package turbo

import (
	"log"
	"net"
)

func closeConn(c net.Conn) (err error) {
	if err = c.Close(); err != nil {
		log.Printf("error closing connection: %v\n", err)
	}
	return err
}
