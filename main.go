package evtWebsocketClient

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

type BasicAuth struct {
	Username string
	Password string
}

// Conn is the connection structure.
type Conn struct {
	OnMessage   func(Msg, *Conn)
	OnError     func(error)
	OnConnected func(*Conn)
	MatchMsg    func(Msg, Msg) bool
	Reconnect   bool
	MsgPrep     func(*Msg)
	BasicAuth   *BasicAuth
	ws          *websocket.Conn
	url         string
	closed      bool
	msgQueue    []Msg
	addToQueue  chan msgOperation

	Dialer        websocket.Dialer
	RequestHeader http.Header

	PingMsg                 []byte
	ComposePingMessage      func() []byte
	PingIntervalSecs        int
	CountPongs              bool
	UnreceivedPingThreshold int
	pingCount               int
	pingTimer               time.Time

	writerAvailable chan struct{}
	readerAvailable chan struct{}
}

// Msg is the message structure.
type Msg struct {
	Body     []byte
	Callback func(Msg, *Conn)
	Params   map[string]interface{}
}

type msgOperation struct {
	add bool
	msg *Msg
	pos int
}

func (c *Conn) onMsg(pkt []byte) {
	msg := Msg{
		Body: pkt,
	}
	if c.MsgPrep != nil {
		c.MsgPrep(&msg)
	}
	var calledBack = false
	if c.MatchMsg != nil {
		queue := make([]Msg, len(c.msgQueue))
		copy(queue, c.msgQueue)
		for _, m := range queue {
			if m.Callback != nil && c.MatchMsg(msg, m) {
				go m.Callback(msg, c)
				defer func() {
					if r := recover(); r != nil {
						fmt.Printf("%v Recovered from error processing message: %v\r\n", time.Now(), r)
					}
				}()
				c.addToQueue <- msgOperation{
					add: false,
					pos: -1, // i,
					msg: &m,
				}
				calledBack = true
				break
			}
		}
	}
	// Fire OnMessage every message that hasnt already been handled in a callback
	if c.OnMessage != nil && !calledBack {
		go c.OnMessage(msg, c)
	}
}

func (c *Conn) onError(err error) {
	if c.OnError != nil {
		c.OnError(err)
	}
	c.close()
}

func (c *Conn) setupPing() {
	if c.PingIntervalSecs > 0 && (c.ComposePingMessage != nil || len(c.PingMsg) > 0) {
		if c.CountPongs && c.OnMessage == nil {
			c.CountPongs = false
		}
		c.pingTimer = time.Now().Add(time.Second * time.Duration(c.PingIntervalSecs))
		go func() {
			for {
				if !time.Now().After(c.pingTimer) {
					time.Sleep(time.Millisecond * 100)
					continue
				}
				if c.closed {
					return
				}
				var msg []byte
				if c.ComposePingMessage != nil {
					msg = c.ComposePingMessage()
				} else {
					msg = c.PingMsg
				}
				if len(msg) > 0 {
					c.write(websocket.TextMessage, msg)
				}
				c.write(websocket.PingMessage, []byte(``))
				if c.CountPongs {
					c.pingCount++
				}
				if c.pingCount > c.UnreceivedPingThreshold+1 {
					c.onError(errors.New("too many pings not responded to"))
					return
				}
				c.pingTimer = time.Now().Add(time.Second * time.Duration(c.PingIntervalSecs))
			}
		}()
	}
}

// PongReceived notify the socket that a ping response (pong) was received, this is left to the user as the message structure can differ between servers
func (c *Conn) PongReceived() {
	c.pingCount--
}

// IsConnected tells wether the connection is
// opened or closed.
func (c *Conn) IsConnected() bool {
	return !c.closed
}

// Send sends a message through the connection.
func (c *Conn) Send(msg Msg) (err error) {
	if msg.Body == nil {
		return errors.New("no message body")
	}
	if c.closed {
		return errors.New("closed connection")
	}
	if msg.Callback != nil && c.addToQueue != nil {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("%v Recovered from error while sending: %v", time.Now(), r)
			}
		}()
		c.addToQueue <- msgOperation{
			add: true,
			pos: -1,
			msg: &msg,
		}
	}
	c.write(websocket.TextMessage, msg.Body)
	return nil
}

func (c *Conn) Close() {
	c.write(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
}

// RemoveFromQueue unregisters a callback from the queue in the event it has timed out
func (c *Conn) RemoveFromQueue(msg Msg) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v Reccovered from error while removing from queue: %v", time.Now(), r)
		}
	}()
	if c.closed {
		return errors.New("closed connection")
	}
	c.addToQueue <- msgOperation{
		add: false,
		pos: -1,
		msg: &msg,
	}
	return nil
}

func (c *Conn) read() bool {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("%v Recovered from error while reading connection: %v\r\n", time.Now(), r)
		}
	}()
	_, ok := <-c.readerAvailable
	if !ok {
		return false
	}
	// m := []wsutil.Message{}
	// m, err := wsutil.ReadServerMessage(c.ws, m)

	// todo: fix-me
	// var buf bytes.Buffer

	op, msg, err := c.ws.ReadMessage()
	if err != nil {
		c.onError(err)
		return false
	}

	c.readerAvailable <- struct{}{}

	/*
		// todo: fix-me ... im not sure what this is supposed to do
		if buf.Len() > 0 {
			_, ok := <-c.writerAvailable
			if ok {
				c.ws.Write(buf.Bytes())
				c.writerAvailable <- struct{}{}
			}
		}
	*/

	if op == websocket.CloseMessage {
		c.close()
		return true
	}

	go c.onMsg(msg)

	// for _, msg := range m {
	// 	switch msg.OpCode {
	// 	case websocket.OpPing:
	// 		go c.write(websocket.OpPong, nil)
	// 	case websocket.OpPong:
	// 		go c.PongReceived()
	// 	case websocket.OpClose:
	// 		c.close()
	// 		return true
	// 	default:
	// 		go c.onMsg(msg.Payload)
	// 	}
	// }

	return true
}

func (c *Conn) write(opcode int, pkt []byte) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("%v Recovered from error while writing to connection %v\r\n", time.Now(), r)
		}
	}()
	_, ok := <-c.writerAvailable
	if !ok {
		return
	}
	err := c.ws.WriteMessage(opcode, pkt)
	if err != nil {
		c.onError(err)
		return
	}
	c.writerAvailable <- struct{}{}
}

func (c *Conn) sendCloseFrame() {
	c.write(websocket.CloseMessage, []byte(``))
}

func (c *Conn) startQueueManager() {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("%v Reccovered from error while processing message queue: %v\r\n", time.Now(), r)
		}
	}()
	for {
		msg, ok := <-c.addToQueue
		if !ok {
			return
		}
		if msg.pos == 0 && msg.msg == nil {
			return
		}
		if msg.add {
			c.msgQueue = append(c.msgQueue, *msg.msg)
		} else {
			if msg.pos >= 0 {
				c.msgQueue = append(c.msgQueue[:msg.pos], c.msgQueue[msg.pos+1:]...)
			} else if c.MatchMsg != nil {
				for i, m := range c.msgQueue {
					if c.MatchMsg(m, *msg.msg) {
						// Delete this element from the queue
						c.msgQueue = append(c.msgQueue[:i], c.msgQueue[i+1:]...)
						break
					}
				}
			}
		}
	}
}

// Disconnect sends a close frame and disconnects from the server
func (c *Conn) Disconnect() {
	c.close()
}

func (c *Conn) close() {
	if c.closed {
		return
	}
	c.closed = true
	c.sendCloseFrame()
	close(c.readerAvailable)
	for _, ok := <-c.readerAvailable; ok; _, ok = <-c.readerAvailable {
	}
	close(c.writerAvailable)
	for _, ok := <-c.writerAvailable; ok; _, ok = <-c.writerAvailable {
	}
	close(c.addToQueue)
	for _, ok := <-c.addToQueue; ok; _, ok = <-c.addToQueue {
	}
	c.addToQueue = nil
	c.ws.Close()

	if c.Reconnect {
		for {
			if err := c.Dial(c.url); err == nil {
				break
			}
			time.Sleep(time.Second * 1)
		}
	}
}

// Dial sets up the connection with the remote
// host provided in the url parameter.
// Note that all the parameters of the structure
// must have been set before calling it.
// tlsconf is optional and provides settings for handling
// connections to tls setvers via wss protocol
func (c *Conn) Dial(url string) error {
	c.closed = true
	c.url = url
	if c.msgQueue == nil {
		c.msgQueue = []Msg{}
	}
	c.readerAvailable = make(chan struct{}, 1)
	c.writerAvailable = make(chan struct{}, 1)
	c.pingCount = 0

	var err error
	if c.BasicAuth != nil {
		if c.RequestHeader == nil {
			c.RequestHeader = make(http.Header)
		}
		c.RequestHeader.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(c.BasicAuth.Username+":"+c.BasicAuth.Password)))
	}
	c.ws, _, err = c.Dialer.DialContext(context.Background(), url, c.RequestHeader)
	if err != nil {
		return err
	}
	c.closed = false
	if c.OnConnected != nil {
		go c.OnConnected(c)
	}

	// setup reader
	go func() {
		for {
			if !c.read() {
				return
			}
		}
	}()

	// setup write channel
	c.addToQueue = make(chan msgOperation) // , 100

	// start que manager
	go c.startQueueManager()

	c.setupPing()

	c.readerAvailable <- struct{}{}
	c.writerAvailable <- struct{}{}

	// resend dropped messages if this is a reconnect
	if len(c.msgQueue) > 0 {
		for _, msg := range c.msgQueue {
			go c.write(websocket.TextMessage, msg.Body)
		}
	}

	return nil
}
