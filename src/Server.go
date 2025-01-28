package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
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
	crashed  bool
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
		crashed:  false,
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

	// Start the periodic finger table update
	go periodicUpdateFingerTable()

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
	mux.HandleFunc("/node-info", nodeInfoHandler)
	mux.HandleFunc("/leave", leaveHandler)
	mux.HandleFunc("/sim-crash", simulateCrashHandler)
	mux.HandleFunc("/sim-recover", simulateRecoverHandler)
	mux.HandleFunc("/join", joinRingHandler)
	mux.HandleFunc("/update-successor", updateSuccessorHandler)
	mux.HandleFunc("/update-predecessor", updatePredecessorHandler)

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
	return s.node.SuccessorID
}

func (s *Server) findClosestPredecessor(key int) *NodeAddress {

	if s.node.FingerTable[0].SuccessorID == nil {
		return s.node.SuccessorID
	}

	// Iterate through the finger table in reverse order
	for i := len(s.node.FingerTable) - 1; i >= 0; i-- {
		finger := s.node.FingerTable[i]

		// fmt.Printf("Checking finger %d: %d\n", i, finger.SuccessorID.Id)

		// Check if the finger points to a node that is a valid predecessor of the key
		// and that the finger node is closer to the key than the current node
		if isBetween(s.node.Id, finger.SuccessorID.Id, key) {
			// fmt.Printf("Found closest predecessor: %d\n", finger.SuccessorID.Id)
			return finger.SuccessorID
		}
	}

	// Return the closest valid predecessor found
	return s.node.FingerTable[len(s.node.FingerTable)-1].SuccessorID
}

// Helper function to check if 'key' is in the interval (n1, n2] with wraparound handling
func isBetweenInclusive(n1, key, n2 int) bool {
	if n1 < n2 {
		return n1 < key && key <= n2
	}
	return n1 < key || key <= n2
}

// Helper function to check if 'key' is in the interval (n1, n2) with wraparound handling
func isBetween(n1, key, n2 int) bool {
	if n1 < n2 {
		return key > n1 && key < n2
	}
	return key > n1 || key < n2
}

func (s *Server) create_info_interface() map[string]interface{} {
	data := make(map[string]interface{})
	data["id"] = s.node.Id
	data["node_hash"] = s.node.Id
	data["address"] = s.node.Address

	if s.node.PredecessorID == nil {
		data["predecessor"] = "nil"
	} else {
		data["predecessor"] = s.node.PredecessorID.Address
	}

	if s.node.SuccessorID == nil {
		data["successor"] = "nil"
	} else {
		data["successor"] = s.node.SuccessorID.Address
	}

	others := make([]string, 0)
	for _, node := range s.node.FingerTable {
		if node.SuccessorID == nil {
			others = append(others, "nil")
		} else {
			others = append(others, node.SuccessorID.Address)
		}
	}

	data["others"] = others
	return data
}

func send_node_info(w http.ResponseWriter, data map[string]interface{}) {
	jsonData, err := json.MarshalIndent(data, "", "\t")

	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Error encoding JSON"))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(jsonData)
}

func get_response(w http.ResponseWriter, url string) *http.Response {

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)

	if err != nil {
		http.Error(w, "Error connecting to successor node", http.StatusInternalServerError)
		return nil
	}

	if resp.StatusCode == http.StatusServiceUnavailable {
		w.WriteHeader(http.StatusServiceUnavailable)
		return nil
	}

	if resp.StatusCode != http.StatusOK {
		http.Error(w, "Error getting node info", http.StatusInternalServerError)
		return nil
	}

	return resp
}

func put_request(w http.ResponseWriter, url string, jsonData []byte) *http.Response {

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest(http.MethodPut, url, strings.NewReader(string(jsonData)))
	if err != nil {
		http.Error(w, "Error creating request", http.StatusInternalServerError)
		return nil
	}

	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "Error connecting to successor node", http.StatusInternalServerError)
		return nil
	}

	if resp.StatusCode == http.StatusServiceUnavailable {
		w.WriteHeader(http.StatusServiceUnavailable)
		return nil
	}

	if resp.StatusCode != http.StatusOK {
		http.Error(w, "Error forwarding request to successor node", http.StatusInternalServerError)
		return nil
	}

	return resp
}

// Additional functions

func updateSuccessor(w http.ResponseWriter, address_from NodeAddress, address_to *NodeAddress) {
	request := fmt.Sprintf("http://%s/update-successor", address_from.Address)
	jsonData, err := json.Marshal(address_to)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Error encoding JSON"))
		return
	}

	resp := put_request(w, request, jsonData)

	if resp == nil {
		return
	}
}

func updatePredecessor(w http.ResponseWriter, address_from NodeAddress, address_to *NodeAddress) {
	request := fmt.Sprintf("http://%s/update-predecessor", address_from.Address)
	jsonData, err := json.Marshal(address_to)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Error encoding JSON"))
		return
	}

	resp := put_request(w, request, jsonData)
	if resp == nil {
		return
	}
}

func getNode(w http.ResponseWriter, address string) map[string]interface{} {
	request := fmt.Sprintf("http://%s/node-info", address)
	resp := get_response(w, request)

	if resp == nil {
		return nil
	}

	var data map[string]interface{}
	decoder := json.NewDecoder(resp.Body)
	err := decoder.Decode(&data)

	if err != nil {
		http.Error(w, "Error decoding JSON", http.StatusInternalServerError)
		return nil
	}

	return data
}

func periodicUpdateFingerTable() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		stabilize()
		checkPredecessor()
		updateFingerTable()
	}
}

func stabilize() {

	// Psudo code
	// 1. x = successor.predecessor
	// 2. if x is between current node and successor
	// 3. 	successor = x
	// 4. notify successor

	s := serverInstance

	successor := s.node.SuccessorID

	// Get the predecessor of the successor node
	request := fmt.Sprintf("http://%s/node-info?successor=%d", successor.Address, s.node.Id)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(request)

	if err != nil {
		return
	}

	var data map[string]interface{}
	decoder := json.NewDecoder(resp.Body)
	err = decoder.Decode(&data)

	if err != nil {
		return
	}

	request = fmt.Sprintf("http://%s/node-info", data["predecessor"].(string))
	client = &http.Client{Timeout: 10 * time.Second}
	resp, err = client.Get(request)

	if err != nil {
		return
	}

	decoder = json.NewDecoder(resp.Body)
	err = decoder.Decode(&data)

	if err != nil {
		return
	}

	predecessor := data

	// Check if the predecessor of the successor node is between the current node and the successor
	if isBetween(s.node.Id, int(predecessor["id"].(float64)), successor.Id) {
		s.node.SuccessorID = &NodeAddress{
			Id:      int(predecessor["id"].(float64)),
			Address: predecessor["address"].(string),
		}
	}

	// Notify the successor node
	notify(successor.Address)
}

func updateFingerTable() {
	// Psudo code
	// next = next + 1
	// if next > m
	// 	next = 1
	// finger[next].node = find_successor(n + 2^(next-1))

	s := serverInstance

	for i := 0; i < keyIdentifierSpace; i++ {

		// Calculate the next finger entry
		next := (s.node.Id + 1<<i) % (1 << keyIdentifierSpace)
		finger := s.node.FingerTable[i]

		successor := s.findSuccessor(next)

		// Get the successor node for the next finger entry
		url := fmt.Sprintf("http://%s/node-info?successor=%d", successor.Address, next)
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Get(url)

		if err != nil || resp.StatusCode != http.StatusOK {
			return
		}

		var data map[string]interface{}
		decoder := json.NewDecoder(resp.Body)
		err = decoder.Decode(&data)

		if err != nil {
			return
		}

		key := "successor_of_" + strconv.Itoa(next)

		node_address := ""
		if data[key] == nil {
			node_address = data["address"].(string)
		} else {
			node_address = data[key].(string)
		}

		finger.SuccessorID = &NodeAddress{
			Id:      int(data["id"].(float64)),
			Address: node_address,
		}
	}
}

func checkPredecessor() {
	// Psudo code
	// if predecessor has failed
	// 	predecessor = nil

	s := serverInstance

	if s.node.PredecessorID == nil {
		return
	}

	request := fmt.Sprintf("http://%s/node-info", s.node.PredecessorID.Address)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(request)

	if err != nil {
		s.node.PredecessorID = nil
		return
	}

	// If the predecessor node has crashed, set the predecessor to nil
	if resp.StatusCode != http.StatusOK {
		s.node.PredecessorID = nil
		return
	}

	var data map[string]interface{}
	decoder := json.NewDecoder(resp.Body)
	err = decoder.Decode(&data)

	if err != nil {
		s.node.PredecessorID = nil
		return
	}

	if data["successor"] != s.node.Address {
		s.node.PredecessorID = nil
	}
}

func notify(address string) {
	// Psudo code
	// if predecessor is nil or n' is between predecessor and n
	// 	predecessor = n'

	s := serverInstance

	request := fmt.Sprintf("http://%s/node-info", address)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(request)

	if err != nil {
		return
	}

	if resp.StatusCode != http.StatusOK {
		return
	}

	var data map[string]interface{}
	decoder := json.NewDecoder(resp.Body)
	err = decoder.Decode(&data)

	if err != nil {
		return
	}

	if s.node.PredecessorID == nil || isBetween(s.node.PredecessorID.Id, int(data["id"].(float64)), s.node.Id) {
		s.node.PredecessorID = &NodeAddress{
			Id:      int(data["id"].(float64)),
			Address: data["address"].(string),
		}
	}
}

func createNewNode() {

	// Creates a new id by hashing a random number
	id := hash(strconv.Itoa(int(time.Now().UnixNano())))

	address := os.Args[3]
	fingerTable := make([]*FingerEntry, keyIdentifierSpace)

	for i := 0; i < keyIdentifierSpace; i++ {
		fingerTable[i] = &FingerEntry{
			Start:       int(math.Pow(2, float64(i))),
			SuccessorID: nil,
		}
	}

	newNode := &Node{
		Id:            id,
		FingerTable:   fingerTable,
		SuccessorID:   &NodeAddress{Id: id, Address: address},
		PredecessorID: nil,
		Address:       address,
	}

	InitServer(newNode)
}

func main() {

	nodeID, err := strconv.Atoi(os.Args[1])
	newNode := os.Args[2]

	if err != nil {
		fmt.Println("Error parsing node ID:", err)
		return
	}

	if newNode == "true" {
		fmt.Println("Created new node")
		keyIdentifierSpace, err = strconv.Atoi(os.Args[4])
		if err != nil {
			fmt.Println("Error parsing key identifier space:", err)
			return
		}

		createNewNode()
	} else {

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
}
