package main

import (
	"fmt"
	"os"

	"example.com/greet/greet"
)

func main() {
	name, err := greet.Parse(os.Args[len(os.Args)-1])
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	fmt.Println(greet.Shout(name))
}
