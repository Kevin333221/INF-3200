package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Node struct {
	Id            int            `json:"id"`
	FingerTable   []*FingerEntry `json:"finger_table"`
	SuccessorID   *NodeAddress   `json:"successorID"`
	PredecessorID *NodeAddress   `json:"predecessorID"`
	Address       string         `json:"address"`
}

type FingerEntry struct {
	Start       int          `json:"start"`
	SuccessorID *NodeAddress `json:"successorID"`
}

type NodeAddress struct {
	Id      int    `json:"id"`
	Address string `json:"address"`
}

type Server struct {
	hostname string
	port     string
	node     *Node
	server   *http.Server
	storage  map[string]string
}

var serverInstance *Server
var keyIdentifierSpace int

func InitServer(node *Node) {

	addressParts := strings.Split(node.Address, ":")

	keyIdentifierSpace = len(node.FingerTable)

	// Create a new server instance
	serverInstance = &Server{
		hostname: addressParts[0],
		port:     addressParts[1],
		node:     node,
		storage:  make(map[string]string),
	}

	serverInstance.server = &http.Server{
		Addr:    ":" + serverInstance.port,
		Handler: initMux(),
	}

	fmt.Printf("\nServer initialized at: %s and node ID %d\n", serverInstance.hostname+":"+serverInstance.port, serverInstance.node.Id)

	// Channel to listen for shutdown signal (interrupts or timer)
	shutdownChan := make(chan os.Signal, 1)
	signal.Notify(shutdownChan, os.Interrupt, syscall.SIGTERM)

	// Start the server
	go startServer()

	// Start the server shutdown timer
	go startServerShutdownTimer(shutdownChan)

	// Wait for the shutdown signal
	<-shutdownChan

	// Shutdown the server
	shutdownServer()

	fmt.Println("Server exiting")
}

func hash(input string) int {

	// Hash the input using SHA-256
	hash := sha256.Sum256([]byte(input))

	// Convert the first 8 bytes of the hash to a uint64
	hashedValue := binary.BigEndian.Uint64(hash[:8])

	// Apply modulo 2^n to restrict the result between 0 and 2^n - 1
	return int(hashedValue % uint64(1<<keyIdentifierSpace))
}

func initMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/helloworld", helloworldHandler)
	mux.HandleFunc("/storage/", storageHandler)
	mux.HandleFunc("/network", networkHandler)
	return mux
}

func shutdownServer() {
	// Shutdown the server
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := serverInstance.server.Shutdown(ctx); err != nil {
		fmt.Println("Server forced to shutdown:", err)
	}
}

func startServer() {
	// Start the server in a separate goroutine
	err := serverInstance.server.ListenAndServe()
	if err != nil && err != http.ErrServerClosed {
		fmt.Printf("Could not listen on %s: %v\n", serverInstance.port, err)
	}
}

func startServerShutdownTimer(shutdownChan chan os.Signal) {
	// Timer to shut down the server after 10 minutes
	time.Sleep(10 * time.Minute)
	fmt.Println("Shutting down the server after 10 minutes...")
	shutdownChan <- os.Interrupt
}

func (s *Server) findSuccessor(key int) *NodeAddress {
	// First, check if the key falls between the current node and its immediate successor
	if isBetweenInclusive(s.node.Id, key, s.node.SuccessorID.Id) {
		return s.node.SuccessorID
	}

	// Otherwise, look in the finger table for the closest predecessor
	closestPredecessor := s.findClosestPredecessor(key)

	// Recursively call findSuccessor on the closest predecessor if it's not nil
	if closestPredecessor != nil {
		return closestPredecessor
	}

	// If no closer predecessor is found, return the successor as fallback
	return s.node.FingerTable[0].SuccessorID
}

func (s *Server) findClosestPredecessor(key int) *NodeAddress {

	// Iterate through the finger table in reverse order
	for i := len(s.node.FingerTable) - 1; i >= 0; i-- {
		finger := s.node.FingerTable[i]

		fmt.Printf("Checking finger %d: %d\n", i, finger.SuccessorID.Id)

		// Check if the finger points to a node that is a valid predecessor of the key
		// and that the finger node is closer to the key than the current node
		if isBetween(s.node.Id, finger.SuccessorID.Id, key) {
			fmt.Printf("Found closest predecessor: %d\n", finger.SuccessorID.Id)
			return finger.SuccessorID
		}
	}

	// Return the closest valid predecessor found
	return s.node.FingerTable[len(s.node.FingerTable)-1].SuccessorID
}

// Helper function to check if 'key' is in the interval (n1, n2] with wraparound handling
func isBetweenInclusive(n1, key, n2 int) bool {
	if n1 < n2 {
		return key > n1 && key <= n2
	}
	return key > n1 || key <= n2
}

// Helper function to check if 'key' is in the interval (n1, n2) with wraparound handling
func isBetween(n1, key, n2 int) bool {
	if n1 < n2 {
		return key > n1 && key < n2
	}
	return key > n1 || key < n2
}

func httpReq(url string) (*http.Response, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	return client.Get(url)
}

func forwardPutStorageRequest(w http.ResponseWriter, address string, key string, value string) {

	// Format the URL
	url := fmt.Sprintf("http://%s/storage/%s", address, key)

	// Forward the request to the given node
	req, err := http.NewRequest("PUT", url, strings.NewReader(value))
	if err != nil {
		http.Error(w, "Error creating request", http.StatusInternalServerError)
		return
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "Error connecting to successor node", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		w.WriteHeader(http.StatusOK)
	} else {
		http.Error(w, "Error forwarding request to successor node", http.StatusInternalServerError)
	}

	// io.Copy(w, resp.Body)
}

// GET: Returns HTTP code 200, with value, if <key> exists in the DHT. Returns HTTP code 404, if <key> does not exist in the DHT.
// PUT: Returns HTTP code 200. Assumed that <value> is persisted
func storageHandler(w http.ResponseWriter, r *http.Request) {
	start := time.Now() // Start timing the request
	s := serverInstance

	if r.Method == "GET" {

		key := strings.TrimPrefix(r.URL.Path, "/storage/")
		keyInt := hash(key)

		log.Printf("Looking up key %d in node %d for request %s\n", keyInt, s.node.Id, r.URL.Path)

		if keyInt < 0 || keyInt >= 1<<keyIdentifierSpace {
			log.Printf("Request %s - Invalid key: %d out of bounds\n", r.URL.Path, keyInt)
			w.WriteHeader(http.StatusNotFound)
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
					return
				} else {
					w.WriteHeader(http.StatusNotFound)
					return
				}
			}
		} else if keyInt > prev_node && keyInt <= curr_node {

			value, ok := s.storage[key]
			if ok {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(value))
				return
			} else {
				w.WriteHeader(http.StatusNotFound)
				return
			}
		}

		successor := s.findSuccessor(keyInt)

		log.Printf("Request %s - Forwarding to successor %d", r.URL.Path, successor.Id)

		url := fmt.Sprintf("http://%s/storage/%s", successor.Address, key)
		resp, err := httpReq(url)
		if err != nil {
			log.Printf("Request %s - Error connecting to successor node: %v", r.URL.Path, err)
			http.Error(w, "Error connecting to successor node", http.StatusInternalServerError)
			return
		}

		if resp.StatusCode == http.StatusOK {
			log.Printf("Request %s - Successfully forwarded to: %s with ID %d", r.URL.Path, successor.Address, successor.Id)
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				log.Printf("Request %s - Error reading response from successor: %v", r.URL.Path, err)
				http.Error(w, "Error reading response from successor node", http.StatusInternalServerError)
				log.Printf("Request %s - Time taken: %v", r.URL.Path, time.Since(start))
				return
			}
			w.WriteHeader(http.StatusOK)
			w.Write(body)
			log.Printf("Request %s - Time taken: %v", r.URL.Path, time.Since(start))
			return
		} else if resp.StatusCode == http.StatusNotFound {
			log.Printf("Request %s - Key not found on: %d", r.URL.Path, successor.Id)

			w.WriteHeader(http.StatusNotFound)

			log.Printf("Request %s - Time taken: %v", r.URL.Path, time.Since(start))
			return
		} else {
			log.Printf("Request %s - Error from successor node", r.URL.Path)
			http.Error(w, "Error connecting to successor node", http.StatusInternalServerError)
			log.Printf("Request %s - Time taken: %v", r.URL.Path, time.Since(start))
			return
		}
	} else if r.Method == "PUT" {

		key := strings.TrimPrefix(r.URL.Path, "/storage/")
		keyInt := hash(key)

		if keyInt < 0 || keyInt >= 1<<keyIdentifierSpace || fmt.Sprintf("%T", keyInt) != "int" {
			log.Printf("Request %s - Invalid key: %d out of bounds or not an integer\n", r.URL.Path, keyInt)
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

		curr_node := s.node.Id
		prev_node := s.node.PredecessorID.Id

		// Checking for wrap-around in the ring
		if prev_node > curr_node {
			if keyInt <= curr_node || keyInt > prev_node {

				// Check local storage
				_, ok := s.storage[key]
				if ok {
					w.WriteHeader(http.StatusForbidden)
					return
				} else {
					s.storage[key] = value
					w.WriteHeader(http.StatusOK)
					return
				}

			}
		} else if keyInt > prev_node && keyInt <= curr_node {

			_, ok := s.storage[key]
			if ok {
				w.WriteHeader(http.StatusForbidden)
				return
			} else {
				s.storage[key] = value
				w.WriteHeader(http.StatusOK)
				return
			}
		}

		successor := s.findSuccessor(keyInt)
		url := fmt.Sprintf("http://%s/storage/%s", successor.Address, key)

		// Forward the request to the given node
		req, err := http.NewRequest("PUT", url, strings.NewReader(value))
		if err != nil {
			http.Error(w, "Error creating request", http.StatusInternalServerError)
			return
		}

		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, "Error connecting to successor node", http.StatusInternalServerError)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			w.WriteHeader(http.StatusOK)
		} else {
			http.Error(w, "Error forwarding request to successor node", http.StatusInternalServerError)
		}

		serverInstance.storage[key] = value
		return

		// key := strings.TrimPrefix(r.URL.Path, "/storage/")
		// hashedKey := hash(key)

		// body, err := io.ReadAll(r.Body)
		// if err != nil {
		// 	fmt.Println("Error reading body:", err)
		// 	w.WriteHeader(http.StatusInternalServerError)
		// 	return
		// }
		// defer r.Body.Close()

		// value := string(body)

		// Check if the hashed key is within the range of the current node

		// // Check if the hashed key is within the range of the current node
		// if hashedKey <= serverInstance.node.Id && hashedKey > serverInstance.node.PredecessorID.Id {
		// 	// If the key already exists in the storage, return 403 Forbidden
		// 	if _, exists := serverInstance.storage[key]; exists {
		// 		w.WriteHeader(http.StatusForbidden)
		// 		w.Header().Set("Content-Type", "text/plain")
		// 		return
		// 	}

		// 	// If not, add the key to the storage
		// 	serverInstance.storage[key] = value
		// 	w.WriteHeader(http.StatusOK)
		// 	return
		// } else if hashedKey > serverInstance.node.Id {
		// 	// If the hashed key is in range of the successor node
		// 	if hashedKey <= serverInstance.node.SuccessorID.Id {
		// 		// Forward to the successor node
		// 		forwardPutStorageRequest(w, serverInstance.node.SuccessorID.Address, key, value)
		// 		return
		// 	}

		// 	//If not in range of the successor node, find the correct successor node
		// 	successor := serverInstance.findSuccessor(hashedKey)
		// 	forwardPutStorageRequest(w, successor.Address, key, value)
		// }
	}
}

// Returns HTTP code 200, with list of known nodes, as a JSON array of strings.
func networkHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		// Collect known node addresses into a list
		nodes := make([]string, 0)
		for _, node := range serverInstance.node.FingerTable {
			nodes = append(nodes, node.SuccessorID.Address)
		}

		// Convert the list of node addresses to JSON
		jsonData, err := json.Marshal(nodes)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("Error encoding JSON"))
			return
		}

		// Set content type and return the JSON data
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(jsonData)
	} else {
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func helloworldHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	} else if r.Method == http.MethodGet {
		w.WriteHeader(http.StatusOK)
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(serverInstance.hostname + ":" + serverInstance.port))
	}
}

func main() {

	// logFile, err := os.OpenFile("dht_performance.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0666)
	// if err != nil {
	// 	log.Fatal(err)
	// }
	// defer logFile.Close()
	// log.SetOutput(logFile)

	nodeID, err := strconv.Atoi(os.Args[1])

	if err != nil {
		fmt.Println("Error parsing node ID:", err)
		return
	}

	// Read data from "Nodes.json"
	file, err := os.Open("DeployServers/Nodes.json")
	if err != nil {
		fmt.Println("Error opening file:", err)
		return
	}
	defer file.Close()

	var nodes []*Node
	decoder := json.NewDecoder(file)
	err = decoder.Decode(&nodes)

	if err != nil {
		fmt.Println("Error decoding JSON:", err)
		return
	}

	var foundNode *Node
	for _, node := range nodes {
		if node.Id == nodeID {
			foundNode = node
			break
		}
	}

	if foundNode == nil {
		fmt.Println("Node not found")
		return
	}

	InitServer(foundNode)
}
