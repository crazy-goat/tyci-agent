package main

import (
	"bufio"
	"os"
)

// readStdin reads all data from stdin until EOF.
// For pipe input (e.g., echo "key" | tyci ...), reads everything.
// For terminal input, reads one line.
func readStdin() ([]byte, error) {
	stat, err := os.Stdin.Stat()
	if err == nil && (stat.Mode()&os.ModeCharDevice) == 0 {
		// Pipe mode: read all until EOF
		buf := make([]byte, 0, 4096)
		tmp := make([]byte, 1024)
		for {
			n, err := os.Stdin.Read(tmp)
			if n > 0 {
				buf = append(buf, tmp[:n]...)
			}
			if err != nil {
				if err.Error() == "EOF" {
					break
				}
				return nil, err
			}
		}
		return buf, nil
	}
	// Terminal mode: read one line
	scanner := bufio.NewScanner(os.Stdin)
	if scanner.Scan() {
		return []byte(scanner.Text()), nil
	}
	return nil, scanner.Err()
}