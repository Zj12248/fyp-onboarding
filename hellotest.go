package main

import (
	"fmt"
	"time"
)

func main() {
	fmt.Println("Go compiler test: Hello, world!")
	fmt.Println("Printing numbers 1 to 5 with 1-second delay:")

	for i := 1; i <= 5; i++ {
		fmt.Println(i)
		time.Sleep(1 * time.Second)
	}

	fmt.Println("Test completed successfully!")
}
