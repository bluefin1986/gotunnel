package main

import (
	"bufio"
	"crypto/tls"
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

var (
	connectionsMap = make(map[string]*ConnectionInfo)
	cmdConn        net.Conn
	mu             sync.Mutex
	muCmd          sync.Mutex
	idCounter      uint64
	logDebug       bool
)

func debugLog(format string, args ...interface{}) {
	if logDebug {
		fmt.Printf(format, args...)
	}
}

func main() {
	controlAddr := flag.String("control", ":6000", "control listen address, e.g. :6000")
	visitorAddr := flag.String("visitor", ":6000", "visitor listen address, e.g. :6001; use same as -control for single-port mode")
	useTLS := flag.Bool("tls", false, "enable TLS listener")
	certFile := flag.String("cert", "", "TLS certificate file")
	keyFile := flag.String("key", "", "TLS private key file")
	flag.BoolVar(&logDebug, "debug", false, "enable debug logs")
	flag.Parse()

	if *useTLS && (*certFile == "" || *keyFile == "") {
		fmt.Println("-cert and -key are required when -tls=true")
		os.Exit(1)
	}

	if *controlAddr == *visitorAddr {
		listener := mustListen(*controlAddr, *useTLS, *certFile, *keyFile)
		defer listener.Close()
		fmt.Printf("gotunnel server listening on %s in single-port mode (control + visitor)\n", *controlAddr)
		acceptLoop(listener, handleSinglePortConn)
		return
	}

	controlListener := mustListen(*controlAddr, *useTLS, *certFile, *keyFile)
	defer controlListener.Close()
	visitorListener := mustListen(*visitorAddr, *useTLS, *certFile, *keyFile)
	defer visitorListener.Close()

	fmt.Printf("gotunnel server control listening on %s\n", *controlAddr)
	fmt.Printf("gotunnel server visitor listening on %s\n", *visitorAddr)

	go acceptLoop(controlListener, handleControlConn)
	acceptLoop(visitorListener, func(conn net.Conn) { handleVisitorConn(conn, nil) })
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
			fmt.Println("Error accepting connection:", err)
			continue
		}
		debugLog("===> new connection %s\n", conn.RemoteAddr())
		go handler(conn)
	}
}

func handleSinglePortConn(conn net.Conn) {
	buf := make([]byte, 1024)
	_ = conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	n, err := conn.Read(buf)
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil && err != io.EOF {
		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			handleVisitorConn(conn, nil)
			return
		}
		fmt.Println("Read error:", err)
		_ = conn.Close()
		return
	}

	first := buf[:n]
	command := strings.TrimSpace(string(first))
	if isControlCommand(command) {
		handleControlCommand(conn, command)
		return
	}
	handleVisitorConn(conn, first)
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
		setCmdConn(conn)
		writeLine(conn, CMD_ACK_SUCC)
		fmt.Printf("Client connected, use it as cmd connection [%s]\n", conn.RemoteAddr())
		go sendHeartbeat(conn)
		go consumeCmdConn(conn)

	case strings.HasPrefix(command, CMD_CONNECT_CHANNEL_RESP):
		connectionPairId := strings.TrimSpace(strings.TrimPrefix(command, CMD_CONNECT_CHANNEL_RESP+CMD_DELIMITER))
		handleTunnelConn(conn, connectionPairId)

	default:
		fmt.Printf("unknown control command [%s] from %s\n", command, conn.RemoteAddr())
		_ = conn.Close()
	}
}

func setCmdConn(conn net.Conn) {
	muCmd.Lock()
	defer muCmd.Unlock()
	if cmdConn != nil && cmdConn != conn {
		_ = cmdConn.Close()
	}
	cmdConn = conn
}

func getCmdConn() net.Conn {
	muCmd.Lock()
	defer muCmd.Unlock()
	return cmdConn
}

func handleVisitorConn(clientConn net.Conn, firstBatch []byte) {
	connectionPairId := generateID()
	connectionInfo := &ConnectionInfo{
		ID:              connectionPairId,
		clientConn:      clientConn,
		firstBatchBytes: append([]byte(nil), firstBatch...),
	}

	mu.Lock()
	connectionsMap[connectionPairId] = connectionInfo
	mu.Unlock()

	fmt.Printf("visitor conn[%s] saved as [%s], ask client to build a tunnel channel\n", clientConn.RemoteAddr(), connectionPairId)

	control := getCmdConn()
	if control == nil {
		fmt.Println("cmdConn is nil, no tunnel client connected")
		cleanupConnection(connectionPairId)
		_ = clientConn.Close()
		return
	}

	if err := writeLine(control, CMD_CONNECT_CHANNEL+CMD_DELIMITER+connectionPairId); err != nil {
		fmt.Println("send connect-channel command failed:", err)
		cleanupConnection(connectionPairId)
		_ = clientConn.Close()
	}
}

func handleTunnelConn(tunnelConn net.Conn, connectionPairId string) {
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

func consumeCmdConn(conn net.Conn) {
	reader := bufio.NewReader(conn)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			muCmd.Lock()
			if cmdConn == conn {
				cmdConn = nil
			}
			muCmd.Unlock()
			_ = conn.Close()
			debugLog("cmd connection closed: %v\n", err)
			return
		}
		command := strings.TrimSpace(line)
		debugLog("Received command-channel message [%s] from %s\n", command, conn.RemoteAddr())
	}
}

func sendHeartbeat(conn net.Conn) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		muCmd.Lock()
		if cmdConn != conn {
			muCmd.Unlock()
			return
		}
		_, err := fmt.Fprintf(conn, "%s\n", CMD_HEART_BEAT)
		muCmd.Unlock()
		if err != nil {
			muCmd.Lock()
			if cmdConn == conn {
				cmdConn = nil
			}
			muCmd.Unlock()
			fmt.Println("Error sending heartbeat:", err)
			_ = conn.Close()
			return
		}
	}
}

func writeLine(conn net.Conn, command string) error {
	muCmd.Lock()
	defer muCmd.Unlock()
	_, err := fmt.Fprintf(conn, "%s\n", command)
	return err
}
