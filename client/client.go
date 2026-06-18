package main

import (
	"bufio"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

const (
	CMD_HEART_BEAT         = "&hb"
	CMD_CLIENT_HELLO       = "&sp"
	CMD_CONNECT_CHANNEL    = "&cc"
	CMD_ACK_SUCC           = "&00"
	CMD_ACK_FAIL           = "&01"
	CMD_BUILD_CHANNEL_RESP = "ccr"
	CMD_DELIMITER          = ":"
)

var (
	serverAddr  string
	localAddr   string
	tunnelName  string
	visitorPort string
	logDebug    bool
	useTLS      bool
	muWrite     sync.Mutex
)

func debugLog(format string, args ...interface{}) {
	if logDebug {
		fmt.Printf(format, args...)
	}
}

func main() {
	flag.StringVar(&serverAddr, "server", "127.0.0.1:6000", "gotunnel server control address, IPv6 should use [addr]:port or addr:port")
	flag.StringVar(&localAddr, "local", "127.0.0.1:22", "local TCP address to expose through tunnel")
	flag.StringVar(&tunnelName, "tunnel", "default", "tunnel name for this connection")
	flag.StringVar(&visitorPort, "visitor", "", "visitor port for this tunnel (required)")
	flag.BoolVar(&useTLS, "tls", false, "connect to server with TLS")
	flag.BoolVar(&logDebug, "debug", false, "enable debug logs")
	flag.Parse()

	cmdConn := makeCommandConn()
	if cmdConn == nil {
		return
	}
	defer cmdConn.Close()

	handleCommand(cmdConn)
}

func makeCommandConn() net.Conn {
	if visitorPort == "" {
		fmt.Println("-visitor port is required (e.g., -visitor 2222)")
		return nil
	}

	cmdConn := connToServer()
	if cmdConn == nil {
		fmt.Println("conn to server failed! check server status")
		return nil
	}

	// Send hello with tunnel name and the visitor port to listen on
	// Format: &sp:<tunnelName>:<visitorPort>
	_, _ = fmt.Fprintf(cmdConn, "%s%s%s%s%s\n", CMD_CLIENT_HELLO, CMD_DELIMITER, tunnelName, CMD_DELIMITER, visitorPort)

	// Wait for ack
	reader := bufio.NewReader(cmdConn)
	line, err := reader.ReadString('\n')
	if err != nil {
		fmt.Println("read ack failed:", err)
		_ = cmdConn.Close()
		return nil
	}
	ack := strings.TrimSpace(line)
	if strings.HasPrefix(ack, CMD_ACK_SUCC) {
		fmt.Printf("tunnel [%s] registered with server\n", tunnelName)
		return cmdConn
	}
	fmt.Printf("server rejected: %s\n", ack)
	_ = cmdConn.Close()
	return nil
}

func handleCommand(cmdConn net.Conn) {
	reader := bufio.NewReader(cmdConn)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err != io.EOF {
				fmt.Println("Read command error:", err)
			}
			return
		}

		command := strings.TrimSpace(line)
		debugLog("Received command:[%s]\n", command)

		switch {
		case strings.HasPrefix(command, CMD_HEART_BEAT):
			debugLog("Received heartbeat, send ack\n")
			if err := writeLine(cmdConn, CMD_ACK_SUCC); err != nil {
				fmt.Println("send heartbeat ack failed:", err)
				return
			}

		case strings.HasPrefix(command, CMD_CONNECT_CHANNEL):
			// &cc:<connId>:<tunnelName>
			parts := strings.SplitN(strings.TrimPrefix(command, CMD_CONNECT_CHANNEL+CMD_DELIMITER), CMD_DELIMITER, 2)
			connectionPairId := parts[0]
			if connectionPairId == "" {
				fmt.Printf("command [%s] is invalid\n", command)
				continue
			}
			fmt.Printf("build tunnel channel for pair id [%s] on tunnel [%s]\n", connectionPairId, tunnelName)
			go buildTunnelChannel(connectionPairId)

		case strings.HasPrefix(command, CMD_ACK_SUCC):
			fmt.Printf("cmd conn built successfully\n")

		case strings.HasPrefix(command, CMD_ACK_FAIL):
			fmt.Printf("server returned failure: %s\n", command)

		default:
			debugLog("unknown command: %s\n", command)
		}
	}
}

func buildTunnelChannel(connectionPairId string) {
	tunnelConn := connToServer()
	if tunnelConn == nil {
		fmt.Println("conn to tunnel failed! check server status")
		return
	}

	// Send build-channel response with tunnel name
	_, _ = fmt.Fprintf(tunnelConn, "%s%s%s%s%s\n", CMD_BUILD_CHANNEL_RESP, CMD_DELIMITER, connectionPairId, CMD_DELIMITER, tunnelName)

	localConn, err := net.Dial("tcp", localAddr)
	if err != nil {
		fmt.Printf("Error connecting to local address %s: %v\n", localAddr, err)
		_ = tunnelConn.Close()
		return
	}

	fmt.Printf("forward tunnel pair [%s] server=%s -> local=%s\n", connectionPairId, tunnelConn.RemoteAddr(), localConn.RemoteAddr())
	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			_ = tunnelConn.Close()
			_ = localConn.Close()
			fmt.Printf("tunnel pair [%s] closed\n", connectionPairId)
		})
	}
	go proxyCopy(localConn, tunnelConn, "server>local", cleanup)
	go proxyCopy(tunnelConn, localConn, "local>server", cleanup)
}

func connToServer() net.Conn {
	dialAddr := normalizeTCPAddr(serverAddr)
	fmt.Printf("trying connect to server %s...\n", dialAddr)
	var conn net.Conn
	var err error
	if useTLS {
		conn, err = tls.Dial("tcp", dialAddr, &tls.Config{InsecureSkipVerify: true})
	} else {
		conn, err = net.DialTimeout("tcp", dialAddr, 10*time.Second)
	}
	if err != nil {
		fmt.Println("Error connecting to server:", err)
		return nil
	}
	fmt.Printf("Connected to server %s\n", dialAddr)
	return conn
}

func normalizeTCPAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return addr
	}

	// Already valid host:port, including bracketed IPv6 like [2001:db8::1]:6000.
	if _, _, err := net.SplitHostPort(addr); err == nil {
		return addr
	}

	// Support convenient unbracketed IPv6 input like 2001:db8::1:6000 by treating
	// the last colon-separated segment as the port and bracketing the host.
	lastColon := strings.LastIndex(addr, ":")
	if lastColon <= 0 || lastColon == len(addr)-1 {
		return addr
	}

	host := addr[:lastColon]
	port := addr[lastColon+1:]
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		return net.JoinHostPort(host, port)
	}
	return addr
}

func proxyCopy(dst net.Conn, src net.Conn, direction string, cleanup func()) {
	defer cleanup()
	n, err := io.Copy(dst, src)
	debugLog("copy finished direction=%s bytes=%d err=%v\n", direction, n, err)
}

func writeLine(conn net.Conn, command string) error {
	muWrite.Lock()
	defer muWrite.Unlock()
	_, err := fmt.Fprintf(conn, "%s\n", command)
	return err
}
