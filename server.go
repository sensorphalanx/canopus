package goap

import (
	"bytes"
	"log"
	"math/rand"
	"net"
	"time"
)

// Server
func NewServer(net string, host string) *Server {
	s := &Server{net: net, host: host}

	s.NewRoute(".well-known/core", GET, func(msg *Message) *Message {
		ack := NewMessageOfType(TYPE_ACKNOWLEDGEMENT, msg.MessageId)
		ack.Code = COAPCODE_205_CONTENT
		ack.AddOption(OPTION_CONTENT_FORMAT, MEDIATYPE_APPLICATION_LINK_FORMAT)

		var buf bytes.Buffer
		for _, r := range s.routes {
			if r.Path != ".well-known/core" {
				buf.WriteString("</" + r.Path + ">;ct=0,")
			}
		}
		ack.Payload = []byte(buf.String())

		return ack
	})

	// Set a MessageID Start
	rand.Seed(42)
	MESSAGEID_CURR = rand.Intn(65535)

	return s
}

type Server struct {
	net        string
	host       string
	messageIds map[uint16]time.Time
	routes     []*Route
}

func (s *Server) Start() error {
	s.messageIds = make(map[uint16]time.Time)

	udpAddr, err := net.ResolveUDPAddr(s.net, s.host)
	if err != nil {
		return err
	}

	conn, err := net.ListenUDP(s.net, udpAddr)
	if err != nil {
		return err
	}

	// Routine for clearing up message IDs which has expired
	ticker := time.NewTicker(MESSAGEID_PURGE_DURATION * time.Second)
	go func() {
		for {
			select {
			case <-ticker.C:
				for k, v := range s.messageIds {
					elapsed := time.Since(v)
					if elapsed > MESSAGEID_PURGE_DURATION {
						delete(s.messageIds, k)
					}
				}
			}
		}
	}()

	readBuf := make([]byte, BUF_SIZE)
	for {
		len, addr, err := conn.ReadFromUDP(readBuf)
		if err == nil {

			msgBuf := make([]byte, len)
			copy(msgBuf, readBuf)

			// Look for route handler matching path and then dispatch
			go s.handleMessage(msgBuf, conn, addr)
		}
	}
}

func (s *Server) handleMessage(msgBuf []byte, conn *net.UDPConn, addr *net.UDPAddr) {
	msg, err := BytesToMessage(msgBuf)
	if err != nil {
		if err == ERR_UNKNOWN_CRITICAL_OPTION {
			SendError402BadOption(msg.MessageId, conn, addr)
		}
	}

	route, err := s.matchingRoute(msg.GetPath(), msg.Code)
	// TODO: Has matching path but not method: HTTP 405 "Method Not Allowed"
	if err == ERR_NO_MATCHING_ROUTE {
		ret := NewMessageOfType(TYPE_ACKNOWLEDGEMENT, msg.MessageId)
		if msg.MessageType == TYPE_NONCONFIRMABLE {
			ret.MessageType = TYPE_NONCONFIRMABLE
		}

		ret.Code = COAPCODE_404_NOT_FOUND
		ret.AddOptions(msg.GetOptions(OPTION_URI_PATH))
		ret.AddOptions(msg.GetOptions(OPTION_CONTENT_FORMAT))

		SendMessage(ret, conn, addr)
		return
	}

	// TODO: Check Content Format if not ALL: 4.15 Unsupported Content-Format

	// Duplicate Message ID Check
	_, dupe := s.messageIds[msg.MessageId]
	if dupe {
		log.Println("Duplicate Message ID ", msg.MessageId)
		if msg.MessageType == TYPE_CONFIRMABLE {
			ret := NewMessageOfType(TYPE_RESET, msg.MessageId)
			ret.Code = COAPCODE_0_EMPTY
			ret.AddOptions(msg.GetOptions(OPTION_URI_PATH))
			ret.AddOptions(msg.GetOptions(OPTION_CONTENT_FORMAT))

			SendMessage(ret, conn, addr)
		}
		return
	}

	if err == nil {
		s.messageIds[msg.MessageId] = time.Now()

		// Auto acknowledge
		if msg.MessageType == TYPE_CONFIRMABLE && route.AutoAck {
			ack := NewMessageOfType(TYPE_ACKNOWLEDGEMENT, msg.MessageId)

			SendMessage(ack, conn, addr)
		}
		resp := route.Handler(msg)

		// TODO: Validate Message before sending (.e.g missing messageId)
		SendMessage(resp, conn, addr)
	}
}

func (s *Server) matchingRoute(path string, method CoapCode) (*Route, error) {
	for _, route := range s.routes {
		if route.Path == path && route.Method == method {
			return route, nil
		}
	}
	return &Route{}, ERR_NO_MATCHING_ROUTE
}
