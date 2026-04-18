package webbus

import (
	"time"

	"github.com/gorilla/websocket"
)

func WriteClosePolicyViolation(conn *websocket.Conn, reason string) error {
	return writeClose(conn, websocket.ClosePolicyViolation, reason)
}

func WriteCloseInternalError(conn *websocket.Conn, reason string) error {
	return writeClose(conn, websocket.CloseInternalServerErr, reason)
}

func writeClose(conn *websocket.Conn, code int, reason string) error {
	return conn.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(code, reason),
		time.Now().Add(time.Second),
	)
}
