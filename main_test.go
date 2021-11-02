package evtWebsocketClient

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

type cstHandler struct{ *testing.T }

type cstServer struct {
	*httptest.Server
	URL string
}

func newServer(t *testing.T) *cstServer {
	var s cstServer
	s.Server = httptest.NewServer(cstHandler{t})
	s.Server.URL += "/"
	s.URL = makeWsProto(s.Server.URL)
	return &s
}

func newTLSServer(t *testing.T) *cstServer {
	var s cstServer
	s.Server = httptest.NewTLSServer(cstHandler{t})
	s.Server.URL += "/"
	s.URL = makeWsProto(s.Server.URL)
	return &s
}

func (t cstHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, _, _, err := ws.UpgradeHTTP(r, w)
	if err != nil {
		t.Logf("Upgrade: %v", err)
		return
	}

	defer conn.Close()

	msg, op, err := wsutil.ReadClientData(conn)
	if err != nil {
		t.Logf("ReadClientData: %v", err)
		return
	}
	err = wsutil.WriteServerMessage(conn, op, msg)
	if err != nil {
		t.Logf("WriteServerMessage: %v", err)
		return
	}
}

func makeWsProto(s string) string {
	return "ws" + strings.TrimPrefix(s, "http")
}

func TestConn_Dial(t *testing.T) {
	simpleWS := newServer(t)
	defer simpleWS.Close()

	tlsWS := newTLSServer(t)
	defer tlsWS.Close()

	type args struct {
		url         string
		subprotocol string
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{
		{
			"ws-normal",
			args{
				simpleWS.URL,
				"",
			},
			false,
		},
		{
			"ws-tls",
			args{
				tlsWS.URL,
				"",
			},
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Conn{}
			var tlsConf *tls.Config
			if tt.name == "ws-tls" {
				tlsConf = &tls.Config{
					InsecureSkipVerify: true,
				}
			}
			if err := c.Dial(tt.args.url, tlsConf); (err != nil) != tt.wantErr {
				t.Errorf("Conn.Dial() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestConn_Send(t *testing.T) {
	simpleWS := newServer(t)
	defer simpleWS.Close()

	type fields struct {
		OnMessage   func(Msg, *Conn)
		OnError     func(error)
		OnConnected func(*Conn)
		MatchMsg    func(Msg, Msg) bool
	}
	type args struct {
		url string
	}
	tests := []struct {
		name   string
		fields fields
		args   args
	}{
		{
			"regular-send",
			fields{
				OnConnected: func(con *Conn) {
					m := Msg{
						Body: []byte("Hello"),
						Callback: func(msg Msg, con *Conn) {
							if string(msg.Body) != "Hello" {
								t.Errorf("Callback() expected = 'Hello', got = '%s'", msg.Body)
							}
						},
					}
					if err := con.Send(m); err != nil {
						t.Errorf("Conn.Send() error = %v", err)
					}
				},
				OnMessage: func(msg Msg, con *Conn) {
					if string(msg.Body) != "Hello" {
						t.Errorf("OnMessage() expected = 'Hello', got = '%s'", msg.Body)
					}
				},
				MatchMsg: func(req, resp Msg) bool {
					return string(req.Body) == string(resp.Body)
				},
				OnError: func(err error) {
					t.Errorf("Error: %v", err)
				},
			},
			args{
				simpleWS.URL,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Conn{
				OnMessage:   tt.fields.OnMessage,
				OnConnected: tt.fields.OnConnected,
				MatchMsg:    tt.fields.MatchMsg,
			}
			err := c.Dial(tt.args.url, nil)
			if err != nil {
				t.Errorf("Conn.Dial() error = %v", err)
			}
			// Wait for response
			time.Sleep(time.Second * 2)
		})
	}
}
