// Command routes prints the registered routes.
package main

import (
	"fmt"

	"example.com/routes/generated"
)

func main() {
	for _, route := range generated.Routes {
		fmt.Println(route)
	}
}
