package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

type ConnInfo struct {
	id   string
	conn net.Conn
	port int
}

var mu sync.Mutex
var heartbeatBytes = []byte("&hb") // + 这是心跳包的字节
var heartbeatLen = len(heartbeatBytes)
var connectionPairMap = make(map[string]net.Conn)

var idCounter int
var localConnInfo *ConnInfo
var serverConnInfo *ConnInfo
var localPort int
var serverAddr string

func sendCommand(clientConn net.Conn, command string) {
	_, err := fmt.Fprintf(clientConn, "%s\n", command)
	if err != nil {
		fmt.Println("Error sending command:", err)
	}
}

func sendLocalPort(clientConn net.Conn, localPort int) {
	_, err := fmt.Fprintf(clientConn, "%d\n", localPort)
	if err != nil {
		fmt.Println("Error sending localPort:", err)
	}
}

func forwardToLocal(localPort int, serverConn net.Conn) {
	localAddr := fmt.Sprintf("127.0.0.1:%d", localPort)

	//尝试连接本地端口，如果连接不成功则进行重试
	localConn, err := net.Dial("tcp", localAddr)
	if err != nil {
		fmt.Printf("Error connecting to local port %d: %v\n", localPort, err)
		return
	}
	mu.Lock()
	var id string
	if serverConnInfo != nil {
		id = serverConnInfo.id
	} else {
		id = generateID()
	}
	localConnInfo = &ConnInfo{
		id:   id,
		conn: localConn,
	}
	mu.Unlock()
	defer localConn.Close()

	// 开始进行端口映射
	errChannel := make(chan error)
	directionChannel := make(chan string)
	go copyData("server>local", true, errChannel, directionChannel)
	go copyData("local>server", false, errChannel, directionChannel)

	select {
	case copyDataErr := <-errChannel:
		direction := <-directionChannel
		if copyDataErr != nil {
			fmt.Printf("【%s】copy data error: %s \n", direction, copyDataErr)
		}
	}
	fmt.Println("forward connection finished")
}

func checkConn(conn net.Conn, isServer bool) (net.Conn, error) {
	buf := make([]byte, 10)
	var writeErr error
	if conn != nil {
		_, err := conn.Write(buf[:0])
		writeErr = err
	}
	if writeErr != nil {
		retryMaxCount := 30
		for attempt := 1; attempt <= retryMaxCount; attempt++ {
			if isServer {
				mu.Lock()
				serverConnInfo = nil
				mu.Unlock()
				fmt.Printf("===retry [%d/%d] conn to server\n", attempt, retryMaxCount)
				newConn := connToServer()
				if newConn != nil {
					fmt.Printf("===Reconnected to server %s successfully, server conn id [%s].\n", serverAddr, serverConnInfo.id)
				} else {
					fmt.Printf("===Reconnected to server %s failed.\n", serverAddr)
					time.Sleep(300 * time.Millisecond)
					continue
				}
				if conn != nil {
					conn.Close()
				}
				return newConn, writeErr
			} else {
				var localConnId string
				if localConnInfo != nil {
					localConnId = localConnInfo.id
				} else {
					localConnId = generateID()
				}
				// 本地连接id和server连接id不匹配，不要重建本地连接了，退出整个重来
				if localConnId != serverConnInfo.id {
					fmt.Printf("localConn id[%s] not match serverConn id[%s].\n", localConnId, serverConnInfo.id)
					break
				}
				localAddr := fmt.Sprintf("127.0.0.1:%d", localPort)
				newConn, errDial := net.Dial("tcp", localAddr)
				mu.Lock()
				localConnInfo = &ConnInfo{
					id:   localConnId,
					conn: newConn,
					port: localPort,
				}
				mu.Unlock()
				if errDial == nil {
					conn.Close()
					fmt.Printf("Reconnected to local %s successfully, local conn id [%s].\n", conn.RemoteAddr(), localConnInfo.id)
					return newConn, writeErr
				}
			}

			fmt.Printf("Error reconnecting (attempt %d): %v\n", attempt, writeErr)
			time.Sleep(1 * time.Second)
		}
		return nil, writeErr
	} else {
		return conn, nil
	}

}

func generateID() string {
	idCounter++
	return fmt.Sprintf("conn%d", idCounter)
}

func copyData(direction string, needCheckConn bool, errChannel chan<- error, directionChannel chan<- string) {
	sourceName := strings.Split(direction, ">")[0]
	srcIsServer := sourceName == "server"
	buf := make([]byte, 1024)
	var dst, src net.Conn
	mu.Lock()
	if srcIsServer {
		src = serverConnInfo.conn
		dst = localConnInfo.conn
	} else {
		src = localConnInfo.conn
		if serverConnInfo == nil {
			errChannel <- errors.New("server connection info is nil")
			directionChannel <- direction
			mu.Unlock()
			return
		}
		dst = serverConnInfo.conn
	}
	mu.Unlock()
	for {
		if needCheckConn {
			// 检查连接是否仍然有效
			tempSrc, errConnSrc := checkConn(src, srcIsServer)
			if errConnSrc != nil {
				if tempSrc != nil {
					src = tempSrc
				} else {
					fmt.Printf("curr direction [%s] Reconnect to source failed.\n", direction)
					break
				}
			}
			tempDst, errConnDst := checkConn(dst, !srcIsServer)
			if errConnDst != nil {
				// tempDst非空，说明重连成功，给赋值回去
				if tempDst != nil {
					dst = tempDst
				} else {
					// tempDst为空，跳出放弃，重来
					fmt.Printf("curr direction [%s] Reconnect to destination failed.\n", direction)
					break
				}
			}
		} else {
			mu.Lock()
			if srcIsServer {
				fmt.Printf("==src server conn changed to %s.\n", serverConnInfo.id)
				src = serverConnInfo.conn
				fmt.Printf("==dst local conn changed to %s.\n", localConnInfo.id)
				dst = localConnInfo.conn
			} else {
				fmt.Printf("++dst server conn changed to %s.\n", serverConnInfo.id)
				dst = serverConnInfo.conn
				fmt.Printf("++src local conn changed to %s.\n", localConnInfo.id)
				src = localConnInfo.conn
			}
			mu.Unlock()
		}

		// 读取数据
		n, err := src.Read(buf)
		if err != nil {
			if err == io.EOF {
				// 连接关闭
				fmt.Printf("Connection closed by the source side. [%s]\n", direction)
				tempSrc, errConnSrc := checkConn(src, !srcIsServer)
				if errConnSrc != nil {
					// tempDst非空，说明重连成功，给赋值回去
					if tempSrc != nil {
						src = tempSrc
						n, err = src.Read(buf)
					} else {
						// tempSrc为空，跳出放弃，重来
						fmt.Printf("curr direction [%s] Reconnect to destination failed.\n", direction)
						break
					}
				}
			} else {
				fmt.Println("Error reading data:", err)
				break
			}
		}

		// 没有bytes需要被转发，跳过
		if n == 0 {
			fmt.Printf("no bytes need to be write [%s], continue.\n", direction)
			continue
		}

		// 判断是否为心跳包, 如果是心跳包不处理跳过
		if bytes.Equal(bytes.TrimSpace(buf[:heartbeatLen]), heartbeatBytes) {
			fmt.Println("Received heartbeat")
			continue
		}

		// 输出调试信息，包括dst和src的IP和端口
		fmt.Printf("Copied %d bytes from %s to %s %s\n", n,
			src.RemoteAddr(),
			dst.RemoteAddr(),
			direction)
		if srcIsServer {
			dst = localConnInfo.conn
		} else {
			dst = serverConnInfo.conn
		}
		// 复制数据
		_, err = dst.Write(buf[:n])
		if err != nil {
			fmt.Println("Error writing data:", err)
			break
		}
	}

	fmt.Printf("Closing copyData goroutine [%s]. \n", direction)
	errChannel <- nil
	directionChannel <- direction
}

func connToServer() net.Conn {
	mu.Lock()
	if serverConnInfo != nil {
		mu.Unlock()
		fmt.Printf("found new server conn [%s], use this conn\n", serverConnInfo.id)
		return serverConnInfo.conn
	}
	fmt.Printf("trying connect to server %s...\n", serverAddr)
	// 连接到服务端
	serverConn, err := net.Dial("tcp", serverAddr)
	if err != nil {
		fmt.Println("Error connecting to server:", err)
		mu.Unlock()
		return nil
	}

	fmt.Printf("Connected to server %s...\n", serverAddr)
	// 发送特殊指令
	command := "sp"
	sendCommand(serverConn, command)
	fmt.Printf("Sent command\n")
	if serverConnInfo == nil {
		id := generateID()
		serverConnInfo = &ConnInfo{
			id:   id,
			conn: serverConn,
			port: localPort,
		}
	}
	time.Sleep(300 * time.Millisecond)
	mu.Unlock()
	return serverConn
}

func main() {
	serverAddr = "192.168.50.192:6000"
	// serverAddr = "127.0.0.1:6000"
	localPort = 5900
	serverConn := connToServer()

	if serverConn == nil {
		fmt.Println("conn to server failed! check server status")
		return
	}
	defer serverConn.Close()

	// 进行端口映射
	for {
		fmt.Println("start forward to local !")
		forwardToLocal(localPort, serverConn)
	}
}
