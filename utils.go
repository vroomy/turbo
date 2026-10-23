package turbo

import (
	"log"
	"net"
)

func closeConn(c net.Conn) {
	if err := c.Close(); err != nil {
		log.Printf("error closing connection: %v\n", err)
	}
}
