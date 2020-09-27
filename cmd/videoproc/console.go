package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"strings"
)

// confirm displays a prompt `s` to the user and returns a bool indicating yes / no
// If the lowercased, trimmed input begins with anything other than 'y', it returns false
// It accepts an int `tries` representing the number of attempts before returning false
func confirm(s string, tries int) bool {
	r := bufio.NewReader(os.Stdin)

	for ; tries > 0; tries-- {
		fmt.Printf("%s [y/n]: ", s)

		res, err := r.ReadString('\n')
		if err != nil {
			log.Fatal(err)
		}

		// Empty input (i.e. "\n")
		if len(res) < 2 {
			continue
		}

		return strings.ToLower(strings.TrimSpace(res))[0] == 'y'
	}

	return false
}

func makeChoice(prompt string, choices []string, tries int) (bool, string) {
	r := bufio.NewReader(os.Stdin)

	sc := strings.Join(choices, "/")
	for ; tries > 0; tries-- {
		fmt.Printf("%s [$s]: ", prompt, sc)

		res, err := r.ReadString('\n')
		if err != nil {
			log.Fatal(err)
		}

		// Empty input (i.e. "\n")
		if len(res) < 2 {
			continue
		}
		res = strings.TrimSpace(res)
		for _, choice := range choices {
			if strings.EqualFold(res, choice) {
				return true, choice
			}
		}
	}

	return false, ""
}
