package main

import (
	"crypto/tls"
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

	CMD_BUILD_CHANNEL = "buildChannel"
)

type CommandType struct {
	cmdName string
	cmd     string
	cmdLen  int
}

var (
	// + 这是服务端发来的开始连接通道的指令
	ConnectChannel = CommandType{
		cmdName: "connect channel",
		cmd:     CMD_CONNECT_CHANNEL,
		cmdLen:  len(CMD_CONNECT_CHANNEL),
	}
	// + 这是心跳包的字节
	HeartBeat = CommandType{
		cmdName: "heartbeat",
		cmd:     CMD_HEART_BEAT,
		cmdLen:  len(CMD_HEART_BEAT),
	}
)

type ConnInfo struct {
	id   string
	conn net.Conn
	port int
}

var (
	mu         sync.Mutex
	muCmd      sync.Mutex
	cmdConn    net.Conn
	localPort  int
	serverAddr string
	logDebug   bool
	useTLS     bool
)

func debugLog(format string, args ...interface{}) {
	if logDebug {
		fmt.Printf(format, args...)
	}
}

func handleCommand(cmdConn net.Conn) {
	// scanner := bufio.NewScanner(cmdConn)
	buf := make([]byte, 128)
	for {
		// 读取数据
		n, err := cmdConn.Read(buf)
		if err != nil {
			if err != io.EOF {
				fmt.Println("Read error:", err)
			}
			break
		}
		command := strings.TrimSpace(string(buf[:n]))
		// 打印收到的command
		debugLog("Received command:[%s]\n", command)
		// 判断是否是以heartBeat的cmd开头
		if strings.HasPrefix(command, CMD_HEART_BEAT) {
			fmt.Printf("Received heartbeat, send received back\n")
			// 发送接收成功的指令CMD_ACK_SUCC
			muCmd.Lock()
			_, err := cmdConn.Write([]byte(CMD_ACK_SUCC))
			muCmd.Unlock()
			if err != nil {
				fmt.Println("send SUCC_ACK failed", err)
				continue
			}
		} else if strings.HasPrefix(command, CMD_CONNECT_CHANNEL) {
			// 指令和指令内容是根据CMD_DELIMITER分割的，把两部分内容分别截取出来，根据content 进行连接转发
			cmdAndContent := strings.Split(command, CMD_DELIMITER)
			if len(cmdAndContent) != 2 {
				fmt.Printf("command [%s] is invalid\n", command)
				continue
			}
			connectionPairId := strings.TrimSpace(cmdAndContent[1])
			//如果connectionPairId 中间包含\n字符，分割以后取第一个
			if strings.Contains(connectionPairId, "\n") {
				connectionPairId = strings.Split(connectionPairId, "\n")[0]
			}
			// 打印connectionPairId
			fmt.Printf("connectionPairId is [%s]\n", connectionPairId)
			// 建立隧道连接
			tunnelConn := connectToTunnel(connectionPairId)
			if tunnelConn == nil {
				fmt.Println("conn to tunnel failed! check tunnel status")
				continue
			}
			// 建立本地连接，和隧道连接进行数据转发
			go forwardToLocal(localPort, tunnelConn)
		} else if strings.HasPrefix(command, CMD_ACK_SUCC) {
			// 收到client hello的 CMD_ACK_SUCC, 打印与服务端成功连接
			fmt.Printf("cmd conn build successfully\n")
		} else {
			debugLog("received %d bytes, not command yet: %s\n", len(command), string(buf[:n]))
		}
		time.Sleep(1 * time.Second)
	}
}

// 连接本机的 localPort
func connectToLocal(localPort int) net.Conn {
	localAddr := fmt.Sprintf("127.0.0.1:%d", localPort)
	localConn, err := net.Dial("tcp", localAddr)
	if err != nil {
		fmt.Printf("Error connecting to local port %d: %v\n", localPort, err)
		return nil
	}
	return localConn
}

func makeCommandConn() net.Conn {
	// 连接到服务端
	cmdConn := connToServer(useTLS)
	if cmdConn == nil {
		fmt.Println("conn to server failed! check server status")
		return nil
	}
	// 发送 CMD_CLIENT_HELLO 指令
	sendCommand(cmdConn, CMD_CLIENT_HELLO)
	return cmdConn
}

// 建立隧道连接
func connectToTunnel(connectionPairId string) net.Conn {
	// 连接serverAddr
	tunnelConn := connToServer(useTLS)
	if tunnelConn == nil {
		fmt.Println("conn to server failed! check server status")
		return nil
	}
	muCmd.Lock()
	// 发送 CMD_BUILD_CHANNEL_RESP 加上 connectionPairId返回给服务端
	_, err := tunnelConn.Write([]byte(CMD_BUILD_CHANNEL_RESP + CMD_DELIMITER + connectionPairId + "\n"))
	// 在这里延迟5毫秒
	time.Sleep(5 * time.Millisecond)
	muCmd.Unlock()
	if err != nil {
		fmt.Println("send CMD_BUILD_CHANNEL_RESP failed", err)
		return nil
	}
	return tunnelConn
}

func connToServer(useTLS bool) net.Conn {
	fmt.Printf("trying connect to server %s...\n", serverAddr)
	var serverConn net.Conn
	var err error
	if useTLS {
		// 连接到服务端
		tlsConfig := &tls.Config{
			// 这里是示例，实际中应该验证服务器证书
			InsecureSkipVerify: true,
		}
		serverConn, err = tls.Dial("tcp", serverAddr, tlsConfig)
	} else {
		serverConn, err = net.Dial("tcp", serverAddr)
	}

	if err != nil {
		fmt.Println("Error connecting to server:", err)
		return nil
	}
	fmt.Printf("Connected to server %s...\n", serverAddr)
	return serverConn
}

func sendCommand(serverConn net.Conn, command string) {
	_, err := fmt.Fprintf(serverConn, "%s\n", command)
	if err != nil {
		fmt.Println("Error sending command:", err)
	}
}

func forwardToLocal(localPort int, serverConn net.Conn) {
	localConn := connectToLocal(localPort)
	if localConn == nil {
		fmt.Println("conn to local failed! check local status")
		return
	}
	muCmd.Lock()
	go copyData(localConn, serverConn, "local>server")
	go copyData(serverConn, localConn, "server>local")
	muCmd.Unlock()
	fmt.Printf("forward server connection %s to %s finished\n", serverConn.RemoteAddr(), localConn.RemoteAddr())
}

func copyData(dst net.Conn, src net.Conn, direction string) {
	defer dst.Close()
	defer src.Close()
	buf := make([]byte, 1024)
	for {
		// 设置读取截止时间为10毫秒，免得断开检测过长
		err := src.SetReadDeadline(time.Now().Add(10 * time.Millisecond))
		if err != nil {
			fmt.Println("Error setting read deadline:", err)
			break
		}

		// 读取数据
		n, err := src.Read(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				// 超时，说明连接还在，继续下一轮循环
				continue
			}

			if err == io.EOF {
				// 连接关闭
				debugLog("Connection closed by the other side.\n")
				// continue
			} else {
				debugLog("Error reading data:%+v\n", err)
			}
			// break
		}

		// 没有bytes需要被转发，跳过, 这时候连接可能处于空闲，不能断开
		if n == 0 {
			// fmt.Printf("no bytes need to be write [%s], continue.\n", direction)
			continue
		}

		// 打印调试信息
		debugLog("Copying %d bytes from %s to %s %s, content:[%s]\n", n, src.RemoteAddr(), dst.RemoteAddr(), direction, string(buf[:n]))

		// 复制数据
		_, err = dst.Write(buf[:n])
		if err != nil {
			fmt.Printf("Error writing data:%+v\n", err)
			// fmt.Println("Error writing data:", err)
			break
		}
	}

	fmt.Printf("Closing copyData %s\n", direction)
}

func main() {
	serverAddr = "192.168.50.192:6000"
	// serverAddr = "127.0.0.1:6000"
	localPort = 5900
	logDebug = false
	useTLS = false

	// 建立指令连接
	cmdConn = makeCommandConn()

	handleCommand(cmdConn)

}
