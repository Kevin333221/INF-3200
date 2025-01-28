package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Endpoints

// GET: Returns HTTP code 200, with value, if <key> exists in the DHT. Returns HTTP code 404, if <key> does not exist in the DHT.
// PUT: Returns HTTP code 200. Assumed that <value> is persisted
func storageHandler(w http.ResponseWriter, r *http.Request) {

	s := serverInstance

	if s.crashed {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}

	if r.Method == "GET" {

		key := strings.TrimPrefix(r.URL.Path, "/storage/")
		keyInt := hash(key)

		// Check if the key is within the valid range
		if keyInt < 0 || keyInt >= 1<<keyIdentifierSpace || fmt.Sprintf("%T", keyInt) != "int" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		// If the current node is the only node in the ring, return the value
		if s.node.PredecessorID == nil {
			_, ok := s.storage[key]
			if ok {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(s.storage[key]))
			} else {
				w.WriteHeader(http.StatusNotFound)
			}
			return
		}

		curr_node := s.node.Id
		prev_node := s.node.PredecessorID.Id

		// Checking for wrap-around in the ring
		if prev_node > curr_node {
			if keyInt <= curr_node || keyInt > prev_node {

				// Check local storage
				value, ok := s.storage[key]
				if ok {
					w.WriteHeader(http.StatusOK)
					w.Write([]byte(value))
				} else {
					w.WriteHeader(http.StatusNotFound)
				}
				return
			}
		} else if keyInt > prev_node && keyInt <= curr_node {

			// If the key falls between the current node and its predecessor, return the value
			value, ok := s.storage[key]
			if ok {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(value))
			} else {
				w.WriteHeader(http.StatusNotFound)
			}
			return
		}

		// Find the successor node for the given key
		successor := s.findSuccessor(keyInt)

		// If the successor is the current node, return the value
		if successor.Address == s.node.Address {
			value, ok := s.storage[key]
			if ok {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(value))
			} else {
				w.WriteHeader(http.StatusNotFound)
			}
			return
		}

		// Forward the request to the successor node
		url := fmt.Sprintf("http://%s/storage/%s", successor.Address, key)
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Get(url)

		if err != nil {
			http.Error(w, "Error connecting to successor node", http.StatusInternalServerError)
			return
		}

		if resp.StatusCode == http.StatusServiceUnavailable {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}

		// Handle the response
		if resp.StatusCode == http.StatusOK {
			body, err := io.ReadAll(resp.Body)

			if err != nil {
				http.Error(w, "Error reading response from successor node", http.StatusInternalServerError)
				return
			}

			w.WriteHeader(http.StatusOK)
			w.Write(body)

		} else if resp.StatusCode == http.StatusNotFound {
			w.WriteHeader(http.StatusNotFound)
		} else {
			http.Error(w, "Error connecting to successor node", http.StatusInternalServerError)
		}
		return

	} else if r.Method == "PUT" {

		key := strings.TrimPrefix(r.URL.Path, "/storage/")
		keyInt := hash(key)

		if keyInt < 0 || keyInt >= 1<<keyIdentifierSpace || fmt.Sprintf("%T", keyInt) != "int" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			fmt.Println("Error reading body:", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		defer r.Body.Close()

		value := string(body)

		// If the current node is the only node in the ring, store the value
		if s.node.PredecessorID == nil {
			_, ok := s.storage[key]
			if ok {
				w.WriteHeader(http.StatusForbidden)
			} else {
				s.storage[key] = value
				w.WriteHeader(http.StatusOK)
			}
			return
		}

		curr_node := s.node.Id
		prev_node := s.node.PredecessorID.Id

		// Checking for wrap-around in the ring
		if prev_node > curr_node {
			if keyInt <= curr_node || keyInt > prev_node {

				// Check local storage
				_, ok := s.storage[key]
				if ok {
					w.WriteHeader(http.StatusForbidden)
				} else {
					// Store the value if the key is not already present
					s.storage[key] = value
					w.WriteHeader(http.StatusOK)
				}
				return
			}
		} else if keyInt > prev_node && keyInt <= curr_node {
			// If the key falls between the current node and its predecessor, store the value
			_, ok := s.storage[key]
			// Store the value only if the key is not already present
			if ok {
				w.WriteHeader(http.StatusForbidden)
			} else {
				s.storage[key] = value
				w.WriteHeader(http.StatusOK)
			}
			return
		}

		// Find the successor node for the given key
		successor := s.findSuccessor(keyInt)

		// If the successor is the current node, store the value
		if successor.Address == s.node.Address {
			_, ok := s.storage[key]
			// Store the value only if the key is not already present
			if ok {
				w.WriteHeader(http.StatusForbidden)
			} else {
				s.storage[key] = value
				w.WriteHeader(http.StatusOK)
			}
			return
		}

		// Forward the request to the successor node
		url := fmt.Sprintf("http://%s/storage/%s", successor.Address, key)

		// Forward the request to the given node
		req, err := http.NewRequest(http.MethodPut, url, strings.NewReader(value))
		if err != nil {
			http.Error(w, "Error creating request", http.StatusInternalServerError)
			return
		}

		// Set the content type and length
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, "Error connecting to successor node", http.StatusInternalServerError)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusServiceUnavailable {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}

		// Handle the response
		if resp.StatusCode == http.StatusOK {
			w.WriteHeader(http.StatusOK)
			// s.storage[key] = value
		} else {
			http.Error(w, "Error forwarding request to successor node", http.StatusInternalServerError)
		}
		return
	}
}

func networkHandler(w http.ResponseWriter, r *http.Request) {

	s := serverInstance

	if s.crashed {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}

	if r.Method == "GET" {

		jsonData, err := json.Marshal(serverInstance.node.FingerTable)

		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("Error encoding JSON"))
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(jsonData)
		return
	}
}

// helloworldHandler handles HTTP requests to the "/helloworld" endpoint.
// It checks the server's status and the request method, and responds accordingly.
//
// If the server is crashed, it responds with a 503 Service Unavailable status.
// If the request method is not GET, it responds with a 405 Method Not Allowed status.
// If the request method is GET, it responds with a 200 OK status and the server's hostname and port.
func helloworldHandler(w http.ResponseWriter, r *http.Request) {

	s := serverInstance
	if s.crashed {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}

	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	} else if r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(serverInstance.hostname + ":" + serverInstance.port))
	}
}

// nodeInfoHandler handles HTTP requests to retrieve information about a node in a distributed system.
// It responds with node information based on the request method and query parameters.
//
// If the server is crashed, it responds with HTTP 503 Service Unavailable.
// If the request method is not GET, it responds with HTTP 405 Method Not Allowed.
//
// For GET requests, it processes the "successor" query parameter to determine the node's successor information.
// If the "successor" query parameter is invalid, it responds with HTTP 400 Bad Request.
//
// The handler checks if the current node is the only node in the ring or if the key falls between the current node
// and its predecessor. If so, it returns the node information.
func nodeInfoHandler(w http.ResponseWriter, r *http.Request) {

	s := serverInstance

	if s.crashed {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}

	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return

	} else if r.Method == http.MethodGet {

		data := s.create_info_interface()
		askingId := r.URL.Query().Get("successor")

		if askingId != "" {

			keyInt, err := strconv.Atoi(askingId)
			key := "successor_of_" + strconv.Itoa(keyInt)

			if err != nil || keyInt < 0 || keyInt >= 1<<keyIdentifierSpace {
				w.WriteHeader(http.StatusBadRequest)
				return
			}

			curr_node := s.node.Id

			// If the current node is the only node in the ring, return it self
			if s.node.SuccessorID.Id == curr_node {
				send_node_info(w, data)
				return
			}

			prev_node := s.node.PredecessorID.Id
			data[key] = s.node.Address

			// Checking for wrap-around in the ring
			if prev_node > curr_node {
				if curr_node >= keyInt || keyInt > prev_node {
					send_node_info(w, data)
					return
				}
			} else if prev_node < keyInt && keyInt <= curr_node {

				// If the key falls between the current node and its predecessor, return the value
				send_node_info(w, data)
				return
			}

			successor := s.findSuccessor(keyInt)

			// If the successor is the current node, return it self
			if successor.Address == s.node.Address {
				send_node_info(w, data)
			} else {

				// Forward the request to the successor node
				url := fmt.Sprintf("http://%s/node-info?successor=%d", successor.Address, keyInt)

				client := &http.Client{Timeout: 10 * time.Second}
				resp, err := client.Get(url)

				if err != nil {
					http.Error(w, "Error connecting to successor node", http.StatusInternalServerError)
					return
				}

				if resp.StatusCode == http.StatusServiceUnavailable {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}

				// Handle the response
				if resp.StatusCode == http.StatusOK {

					var successorData map[string]interface{}
					decoder := json.NewDecoder(resp.Body)
					err = decoder.Decode(&successorData)

					if err != nil {
						http.Error(w, "Error decoding JSON", http.StatusInternalServerError)
						return
					}

					data["address"] = successorData["address"]
					data["successor"] = successorData["successor"]
					data["predecessor"] = successorData["predecessor"]
					data["others"] = successorData["others"]
					data["node_hash"] = successorData["node_hash"]
					data[key] = successorData[key]
					send_node_info(w, data)
					return

				} else if resp.StatusCode == http.StatusNotFound {
					w.WriteHeader(http.StatusNotFound)
				} else {
					http.Error(w, "Error connecting to successor node", http.StatusInternalServerError)
				}
			}
		}
		send_node_info(w, data)
		return
	}
}

// updateSuccessorHandler handles HTTP PUT requests to update the successor of the current node.
// It expects a JSON payload containing the new successor's node address.
// If the request method is not PUT or the JSON payload is invalid, it responds with a 400 Bad Request status.
// On successful update, it responds with a 200 OK status.
//
// Request Body:
//
//	{
//	  "Id": "string", // The ID of the new successor node
//	  "Address": "string" // The address of the new successor node
//	}
//
// Response Codes:
// 200 OK - Success
// 400 Bad Request - Invalid request method or JSON payload
func updateSuccessorHandler(w http.ResponseWriter, r *http.Request) {

	if r.Method != http.MethodPut {
		return
	}

	var node *NodeAddress
	err := json.NewDecoder(r.Body).Decode(&node)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Invalid JSON"))
		return
	}

	if node == nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Invalid JSON"))
		return
	}

	// Update the successor of the current node
	serverInstance.node.SuccessorID = node

	w.WriteHeader(http.StatusOK)
}

// updatePredecessorHandler handles HTTP PUT requests to update the predecessor of the current node.
// It expects a JSON body containing the new predecessor's node address.
// If the request method is not PUT or the JSON is invalid, it responds with a 400 Bad Request status.
// On success, it updates the predecessor and responds with a 200 OK status.
func updatePredecessorHandler(w http.ResponseWriter, r *http.Request) {

	if r.Method != http.MethodPut {
		return
	}

	var node *NodeAddress
	err := json.NewDecoder(r.Body).Decode(&node)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Invalid JSON"))
		return
	}

	if node == nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Invalid JSON"))
		return
	}

	// Update the predecessor of the current node
	serverInstance.node.PredecessorID = node

	w.WriteHeader(http.StatusOK)
}

// joinRingHandler handles the HTTP request for a node to join the ring.
// It performs the following steps:
// 1. Checks if the server is crashed and returns a 503 Service Unavailable status if true.
// 2. Validates that the request method is POST, otherwise returns a 405 Method Not Allowed status.
// 3. Retrieves the successor node ID from the query parameters and returns a 400 Bad Request status if not provided.
// 4. Sends a request to the successor node to get its information.
// 5. Decodes the JSON response from the successor node.
// 6. Updates the current node's successor with the successor node's successor information.
// 7. Updates the current node's predecessor with the successor node's predecessor information.
// 8. Updates the predecessor's successor to the current node.
// 9. Updates the successor's predecessor to the current node.
//
// Parameters:
// - w: http.ResponseWriter to write the HTTP response.
// - r: *http.Request containing the HTTP request.
//
// Note: This handler assumes the existence of several helper functions such as get_response, getNode, updateSuccessor, and updatePredecessor.
func joinRingHandler(w http.ResponseWriter, r *http.Request) {

	s := serverInstance

	if s.crashed {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return

	} else if r.Method == http.MethodPost {

		successorID := r.URL.Query().Get("nprime")
		if successorID == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		// Sending a request to the successor node to get the node info
		nodeInfo := fmt.Sprintf("http://%s/node-info?successor=%d", successorID, s.node.Id)
		resp := get_response(w, nodeInfo)

		if resp == nil {
			return
		}

		// Decode the JSON response
		var data map[string]interface{}
		decoder := json.NewDecoder(resp.Body)
		err := decoder.Decode(&data)

		if err != nil {
			http.Error(w, "Error decoding JSON", http.StatusInternalServerError)
			return
		}

		// Update the successor of the current node
		key := "successor_of_" + strconv.Itoa(s.node.Id)

		node_address := ""
		if data[key] == nil {
			node_address = data["address"].(string)
		} else {
			node_address = data[key].(string)
		}

		successorNode := getNode(w, node_address)

		// Update the current nodes successor to the successor nodes successor
		s.node.SuccessorID = &NodeAddress{
			Id:      int(successorNode["id"].(float64)),
			Address: successorNode["address"].(string),
		}

		if successorNode["predecessor"] == "nil" {
			successorNode["predecessor"] = successorNode["address"]
		}

		// Update the current nodes predecessor to the successor nodes predecessor
		s := serverInstance
		predecessorData := getNode(w, successorNode["predecessor"].(string))

		// Update the current nodes predecessor to the successor nodes predecessor
		s.node.PredecessorID = &NodeAddress{
			Id:      int(predecessorData["id"].(float64)),
			Address: predecessorData["address"].(string),
		}

		address := &NodeAddress{
			Id:      s.node.Id,
			Address: s.node.Address,
		}

		// Update my predecessor's successor to me
		updateSuccessor(w, *s.node.PredecessorID, address)

		// Update the predecessor of the successor node
		updatePredecessor(w, *s.node.SuccessorID, address)

		//////////////////////////////////////////////
		/////									//////
		/////		Maybe add key transfer		//////
		/////									//////
		//////////////////////////////////////////////
	}
}

func leaveHandler(w http.ResponseWriter, r *http.Request) {

	s := serverInstance

	if s.crashed {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	// If the current node is the only node in the ring, the state is already correct
	if s.node.PredecessorID == nil {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Update the successor of the current node
	updateSuccessor(w, *s.node.PredecessorID, s.node.SuccessorID)

	// Update the predecessor of the successor node
	updatePredecessor(w, *s.node.SuccessorID, s.node.PredecessorID)

	// Remove the current node from the ring
	s.node.PredecessorID = nil
	s.node.SuccessorID = &NodeAddress{
		Id:      s.node.Id,
		Address: s.node.Address,
	}

	// Reset the finger table
	for finger := range s.node.FingerTable {
		s.node.FingerTable[finger].SuccessorID = nil
	}

	w.WriteHeader(http.StatusOK)
}

func simulateCrashHandler(w http.ResponseWriter, r *http.Request) {

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	} else if r.Method == http.MethodPost {

		/*
			Simulate a crash.

			The node must stop processing requests
			from other nodes, without notifying them first. Any request or normal
			operational messages between nodes should be either completely refused or
			responded to with an error code without being acted upon. The "crashed"
			node must respond only to the /sim-recover call. Your network must
			detect that the node is not responding, and rearrange itself as if the node
			has left.
		*/

		serverInstance.crashed = true
		w.WriteHeader(http.StatusOK)
	}
}

func simulateRecoverHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	} else if r.Method == http.MethodPost {

		/*
			Simulate a crash recovery.

			The node must "recover" from
			its simulated crashed state and begin responding again to requests from
			other nodes. If the crashed node has been excluded from the network, it
			should request to re-join the network via one of its previous neighbors
		*/

		if serverInstance.crashed {
			serverInstance.crashed = false
			w.WriteHeader(http.StatusOK)
		}
	}
}
