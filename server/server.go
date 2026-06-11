package main

import (
	"bufio"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	CMD_HEART_BEAT           = "&hb"
	CMD_CLIENT_HELLO         = "&sp"
	CMD_CONNECT_CHANNEL      = "&cc"
	CMD_ACK_SUCC             = "&00"
	CMD_ACK_FAIL             = "&01"
	CMD_CONNECT_CHANNEL_RESP = "ccr"
	CMD_DELIMITER            = ":"
)

type ConnectionInfo struct {
	ID              string
	tunnelConn      net.Conn
	clientConn      net.Conn
	firstBatchBytes []byte
}

type TunnelClient struct {
	Name     string
	CmdConn  net.Conn
	Listener net.Listener
	MuCmd    sync.Mutex
}

var (
	connectionsMap = make(map[string]*ConnectionInfo)
	tunnelClients  = make(map[string]*TunnelClient)
	mu             sync.Mutex
	muRegistry     sync.Mutex
	idCounter      uint64
	logDebug       bool
)

func debugLog(format string, args ...interface{}) {
	if logDebug {
		fmt.Printf(format, args...)
	}
}

func main() {
	controlAddr := flag.String("control", ":6000", "control listen address")
	useTLS := flag.Bool("tls", false, "enable TLS listener")
	certFile := flag.String("cert", "", "TLS certificate file")
	keyFile := flag.String("key", "", "TLS private key file")
	flag.BoolVar(&logDebug, "debug", false, "enable debug logs")
	flag.Parse()

	if *useTLS && (*certFile == "" || *keyFile == "") {
		fmt.Println("-cert and -key are required when -tls=true")
		os.Exit(1)
	}

	controlListener := mustListen(*controlAddr, *useTLS, *certFile, *keyFile)
	defer controlListener.Close()
	fmt.Printf("gotunnel server control listening on %s\n", *controlAddr)
	fmt.Println("waiting for clients to register tunnels...")
	acceptLoop(controlListener, handleControlConn)
}

func mustListen(addr string, useTLS bool, certFile string, keyFile string) net.Listener {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Printf("Error listening on %s: %v\n", addr, err)
		os.Exit(1)
	}
	if !useTLS {
		return listener
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		fmt.Println("Error loading certificate:", err)
		os.Exit(1)
	}
	return tls.NewListener(listener, &tls.Config{Certificates: []tls.Certificate{cert}})
}

func acceptLoop(listener net.Listener, handler func(net.Conn)) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				debugLog("listener closed, acceptLoop exiting\n")
				return
			}
			fmt.Println("Error accepting connection:", err)
			continue
		}
		debugLog("===> new connection %s\n", conn.RemoteAddr())
		go handler(conn)
	}
}

func isControlCommand(command string) bool {
	return strings.HasPrefix(command, CMD_CLIENT_HELLO) ||
		strings.HasPrefix(command, CMD_CONNECT_CHANNEL_RESP) ||
		strings.HasPrefix(command, CMD_ACK_SUCC) ||
		strings.HasPrefix(command, CMD_ACK_FAIL)
}

func handleControlConn(conn net.Conn) {
	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		fmt.Println("Read control command error:", err)
		_ = conn.Close()
		return
	}
	handleControlCommand(conn, strings.TrimSpace(line))
}

func handleControlCommand(conn net.Conn, command string) {
	fmt.Printf("Received command:[%s] from %s\n", command, conn.RemoteAddr())
	switch {
	case strings.HasPrefix(command, CMD_CLIENT_HELLO):
		// &sp:<tunnelName>:<visitorPort>
		parts := strings.SplitN(command, CMD_DELIMITER, 4)
		tunnelName := "default"
		visitorPort := ""
		if len(parts) >= 2 && parts[1] != "" {
			tunnelName = parts[1]
		}
		if len(parts) >= 3 && parts[2] != "" {
			visitorPort = parts[2]
		}
		if visitorPort == "" {
			fmt.Printf("client missing visitor port, command: %s\n", command)
			writeLine(conn, CMD_ACK_FAIL+CMD_DELIMITER+"missing visitor port")
			_ = conn.Close()
			return
		}
		registerTunnelClient(tunnelName, visitorPort, conn)

	case strings.HasPrefix(command, CMD_CONNECT_CHANNEL_RESP):
		// ccr:<connId>:<tunnelName>
		parts := strings.SplitN(strings.TrimPrefix(command, CMD_CONNECT_CHANNEL_RESP+CMD_DELIMITER), CMD_DELIMITER, 2)
		connectionPairId := parts[0]
		tunnelName := "default"
		if len(parts) >= 2 {
			tunnelName = parts[1]
		}
		handleTunnelConn(conn, connectionPairId, tunnelName)

	default:
		fmt.Printf("unknown control command [%s] from %s\n", command, conn.RemoteAddr())
		_ = conn.Close()
	}
}

func closeTunnelListener(tunnelName string) {
	if client, ok := tunnelClients[tunnelName]; ok {
		if client.Listener != nil {
			_ = client.Listener.Close()
			client.Listener = nil
		}
	}
}

func registerTunnelClient(tunnelName, visitorPort string, cmdConn net.Conn) {
	muRegistry.Lock()
	defer muRegistry.Unlock()

	// Close existing tunnel (listener + cmd connection)
	if old, exists := tunnelClients[tunnelName]; exists {
		fmt.Printf("replacing existing client for tunnel [%s]\n", tunnelName)
		if old.Listener != nil {
			_ = old.Listener.Close()
		}
		_ = old.CmdConn.Close()
		delete(tunnelClients, tunnelName)
	}

	// Create visitor listener BEFORE storing the client
	addr := ":" + strings.TrimPrefix(visitorPort, ":")
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Printf("ERROR: failed to listen on %s for tunnel [%s]: %v\n", addr, tunnelName, err)
		writeLine(cmdConn, CMD_ACK_FAIL+CMD_DELIMITER+"listen failed: "+err.Error())
		_ = cmdConn.Close()
		return
	}

	// Now store the client with the listener
	client := &TunnelClient{
		Name:     tunnelName,
		CmdConn:  cmdConn,
		Listener: listener,
	}
	tunnelClients[tunnelName] = client

	fmt.Printf("tunnel [%s] registered, visitor listening on %s, client=%s\n", tunnelName, addr, cmdConn.RemoteAddr())
	writeLine(cmdConn, CMD_ACK_SUCC+CMD_DELIMITER+tunnelName)

	go sendHeartbeatForTunnel(tunnelName, cmdConn)
	go consumeCmdConnForTunnel(tunnelName, cmdConn)
	go acceptLoop(listener, func(conn net.Conn) { handleVisitorConn(conn, nil, tunnelName) })
}

func getTunnelClient(tunnelName string) *TunnelClient {
	muRegistry.Lock()
	defer muRegistry.Unlock()
	return tunnelClients[tunnelName]
}

func handleVisitorConn(clientConn net.Conn, firstBatch []byte, tunnelName string) {
	connectionPairId := generateID()
	connectionInfo := &ConnectionInfo{
		ID:              connectionPairId,
		clientConn:      clientConn,
		firstBatchBytes: append([]byte(nil), firstBatch...),
	}

	mu.Lock()
	connectionsMap[connectionPairId] = connectionInfo
	mu.Unlock()

	fmt.Printf("visitor conn[%s] saved as [%s] for tunnel [%s]\n", clientConn.RemoteAddr(), connectionPairId, tunnelName)

	client := getTunnelClient(tunnelName)
	if client == nil {
		fmt.Printf("no client for tunnel [%s]\n", tunnelName)
		cleanupConnection(connectionPairId)
		_ = clientConn.Close()
		return
	}

	client.MuCmd.Lock()
	err := writeLine(client.CmdConn, CMD_CONNECT_CHANNEL+CMD_DELIMITER+connectionPairId+CMD_DELIMITER+tunnelName)
	client.MuCmd.Unlock()
	if err != nil {
		fmt.Println("send connect-channel command failed:", err)
		cleanupConnection(connectionPairId)
		_ = clientConn.Close()
	}
}

func handleTunnelConn(tunnelConn net.Conn, connectionPairId string, tunnelName string) {
	if connectionPairId == "" {
		fmt.Println("empty connectionPairId")
		writeLine(tunnelConn, CMD_ACK_FAIL+CMD_DELIMITER+"empty connectionPairId")
		_ = tunnelConn.Close()
		return
	}

	mu.Lock()
	connectionInfo, exists := connectionsMap[connectionPairId]
	if exists {
		connectionInfo.tunnelConn = tunnelConn
	}
	mu.Unlock()

	if !exists {
		errmsg := fmt.Sprintf("connectionPairId[%s] does not exist", connectionPairId)
		fmt.Println(errmsg)
		writeLine(tunnelConn, CMD_ACK_FAIL+CMD_DELIMITER+errmsg)
		_ = tunnelConn.Close()
		return
	}

	if len(connectionInfo.firstBatchBytes) > 0 {
		if _, err := connectionInfo.tunnelConn.Write(connectionInfo.firstBatchBytes); err != nil {
			fmt.Printf("Error writing firstBatchBytes to tunnelConn:%+v\n", err)
			cleanupConnection(connectionPairId)
			return
		}
	}
	establishDataChannel(connectionInfo)
}

func establishDataChannel(connectionInfo *ConnectionInfo) {
	fmt.Printf("Data channel established [%s] visitor=%s tunnel=%s\n", connectionInfo.ID, connectionInfo.clientConn.RemoteAddr(), connectionInfo.tunnelConn.RemoteAddr())
	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			cleanupConnection(connectionInfo.ID)
			_ = connectionInfo.clientConn.Close()
			_ = connectionInfo.tunnelConn.Close()
			fmt.Printf("Data channel closed [%s]\n", connectionInfo.ID)
		})
	}
	go proxyCopy(connectionInfo.clientConn, connectionInfo.tunnelConn, "tunnel>visitor", cleanup)
	go proxyCopy(connectionInfo.tunnelConn, connectionInfo.clientConn, "visitor>tunnel", cleanup)
}

func proxyCopy(dst net.Conn, src net.Conn, direction string, cleanup func()) {
	defer cleanup()
	n, err := io.Copy(dst, src)
	debugLog("copy finished direction=%s bytes=%d err=%v\n", direction, n, err)
}

func cleanupConnection(id string) {
	mu.Lock()
	delete(connectionsMap, id)
	mu.Unlock()
}

func generateID() string {
	id := atomic.AddUint64(&idCounter, 1)
	return fmt.Sprintf("conn%d", id)
}

func removeTunnelClient(tunnelName string, conn net.Conn) {
	muRegistry.Lock()
	defer muRegistry.Unlock()
	if client, ok := tunnelClients[tunnelName]; ok && client.CmdConn == conn {
		if client.Listener != nil {
			_ = client.Listener.Close()
		}
		delete(tunnelClients, tunnelName)
		fmt.Printf("tunnel [%s] removed\n", tunnelName)
	}
}

func consumeCmdConnForTunnel(tunnelName string, conn net.Conn) {
	reader := bufio.NewReader(conn)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			removeTunnelClient(tunnelName, conn)
			_ = conn.Close()
			return
		}
		command := strings.TrimSpace(line)
		debugLog("tunnel [%s] command-channel message: %s\n", tunnelName, command)
	}
}

func sendHeartbeatForTunnel(tunnelName string, conn net.Conn) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		muRegistry.Lock()
		client, ok := tunnelClients[tunnelName]
		if !ok || client.CmdConn != conn {
			muRegistry.Unlock()
			return
		}
		_, err := fmt.Fprintf(conn, "%s\n", CMD_HEART_BEAT)
		muRegistry.Unlock()
		if err != nil {
			removeTunnelClient(tunnelName, conn)
			_ = conn.Close()
			return
		}
	}
}

func writeLine(conn net.Conn, command string) error {
	_, err := fmt.Fprintf(conn, "%s\n", command)
	return err
}
