// Copyright 2019 Google LLC
//
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file or at
// https://developers.google.com/open-source/licenses/bsd

package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	_log "log"
	"os"
	"strings"

	"filippo.io/age"
	"golang.org/x/crypto/ssh/terminal"
)

type multiFlag []string

func (f *multiFlag) String() string { return fmt.Sprint(*f) }

func (f *multiFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}

const usage = `Usage:
    age -r RECIPIENT [-a] [-o OUTPUT] [INPUT]
    age --decrypt [-i KEY] [-o OUTPUT] [INPUT]

Options:
    -o, --output OUTPUT         Write the result to the file at path OUTPUT.
    -a, --armor                 Encrypt to a PEM encoded format.
    -p, --passphrase            Encrypt with a passphrase.
    -r, --recipient RECIPIENT   Encrypt to the specified RECIPIENT. Can be repeated.
    -d, --decrypt               Decrypt the input to the output.
    -i, --identity KEY          Use the private key file at path KEY. Can be repeated.

INPUT defaults to standard input, and OUTPUT defaults to standard output.

RECIPIENT can be an age public key, as generated by age-keygen, ("age1...")
or an SSH public key ("ssh-ed25519 AAAA...", "ssh-rsa AAAA...").

KEY is a path to a file with age secret keys, one per line
(ignoring "#" prefixed comments and empty lines), or to an SSH key file.
Multiple keys can be provided, and any unused ones will be ignored.

Example:
    $ age-keygen -o key.txt
    Public key: age1ql3z7hjy54pw3hyww5ayyfg7zqgvc7w3j2elw8zmrj2kg5sfn9aqmcac8p
    $ tar cvz ~/data | age -r age1ql3z7hjy54pw3hyww5ayyfg7zqgvc7w3j2elw8zmrj2kg5sfn9aqmcac8p > data.tar.gz.age
    $ age -d -i key.txt -o data.tar.gz data.tar.gz.age`

func main() {
	_log.SetFlags(0)
	flag.Usage = func() { fmt.Fprintf(os.Stderr, "%s\n", usage) }

	var (
		outFlag                          string
		decryptFlag, armorFlag, passFlag bool
		recipientFlags, identityFlags    multiFlag
	)

	flag.BoolVar(&decryptFlag, "d", false, "decrypt the input")
	flag.BoolVar(&decryptFlag, "decrypt", false, "decrypt the input")
	flag.BoolVar(&passFlag, "p", false, "use a passphrase")
	flag.BoolVar(&passFlag, "passphrase", false, "use a passphrase")
	flag.StringVar(&outFlag, "o", "", "output to `FILE` (default stdout)")
	flag.StringVar(&outFlag, "output", "", "output to `FILE` (default stdout)")
	flag.BoolVar(&armorFlag, "a", false, "generate an armored file")
	flag.BoolVar(&armorFlag, "armor", false, "generate an armored file")
	flag.Var(&recipientFlags, "r", "recipient (can be repeated)")
	flag.Var(&recipientFlags, "recipient", "recipient (can be repeated)")
	flag.Var(&identityFlags, "i", "identity (can be repeated)")
	flag.Var(&identityFlags, "identity", "identity (can be repeated)")
	flag.Parse()

	if flag.NArg() > 1 {
		logFatalf("Error: too many arguments.\n" +
			"age accepts a single optional argument for the input file.")
	}
	switch {
	case decryptFlag:
		if armorFlag {
			logFatalf("Error: -a/--armor can't be used with -d/--decrypt.\n" +
				"Note that armored files are detected automatically.")
		}
		if passFlag {
			logFatalf("Error: -p/--passphrase can't be used with -d/--decrypt.\n" +
				"Note that password protected files are detected automatically.")
		}
		if len(recipientFlags) > 0 {
			logFatalf("Error: -r/--recipient can't be used with -d/--decrypt.\n" +
				"Did you mean to use -i/--identity to specify a private key?")
		}
	default: // encrypt
		if len(identityFlags) > 0 {
			logFatalf("Error: -i/--identity can't be used in encryption mode.\n" +
				"Did you forget to specify -d/--decrypt?")
		}
		if len(recipientFlags) == 0 && !passFlag {
			logFatalf("Error: missing recipients.\n" +
				"Did you forget to specify -r/--recipient or -p/--passphrase?")
		}
		if len(recipientFlags) > 0 && passFlag {
			logFatalf("Error: -p/--passphrase can't be combined with -r/--recipient.")
		}
	}

	var in, out io.ReadWriter = os.Stdin, os.Stdout
	if name := flag.Arg(0); name != "" && name != "-" {
		f, err := os.Open(name)
		if err != nil {
			logFatalf("Error: failed to open input file %q: %v", name, err)
		}
		defer f.Close()
		in = f
	} else {
		stdinInUse = true
	}
	if name := outFlag; name != "" && name != "-" {
		f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0666)
		if err != nil {
			logFatalf("Error: failed to open output file %q: %v", name, err)
		}
		defer f.Close()
		out = f
	} else if terminal.IsTerminal(int(os.Stdout.Fd())) && !decryptFlag {
		if armorFlag {
			// If the output will go to a TTY, and it will be armored, buffer it
			// up so it doesn't get in the way of typing the input.
			buf := &bytes.Buffer{}
			defer func() { io.Copy(os.Stdout, buf) }()
			out = buf
		} else if name != "-" {
			// If the output wouldn't be armored, refuse to send binary to the
			// terminal unless explicitly requested with "-o -".
			logFatalf("Error: refusing to output binary to the terminal.\n" +
				`Did you mean to use -a/--armor? Force with "-o -".`)
		}
	}

	switch {
	case decryptFlag:
		decrypt(identityFlags, in, out)
	case passFlag:
		pass, err := passphrasePromptForEncryption()
		if err != nil {
			logFatalf("Error: %v", err)
		}
		encryptPass(pass, in, out, armorFlag)
	default:
		encryptKeys(recipientFlags, in, out, armorFlag)
	}
}

func passphrasePromptForEncryption() (string, error) {
	fmt.Fprintf(os.Stderr, "Enter passphrase (leave empty to autogenerate a secure one): ")
	pass, err := readPassphrase()
	if err != nil {
		return "", fmt.Errorf("could not read passphrase: %v", err)
	}
	p := string(pass)
	if p == "" {
		var words []string
		for i := 0; i < 10; i++ {
			words = append(words, randomWord())
		}
		p = strings.Join(words, "-")
		fmt.Fprintf(os.Stderr, "Using the autogenerated passphrase %q.\n", p)
	} else {
		fmt.Fprintf(os.Stderr, "Confirm passphrase: ")
		confirm, err := readPassphrase()
		if err != nil {
			return "", fmt.Errorf("could not read passphrase: %v", err)
		}
		if string(confirm) != p {
			return "", fmt.Errorf("passphrases didn't match")
		}
	}
	return p, nil
}

func encryptKeys(keys []string, in io.Reader, out io.Writer, armor bool) {
	var recipients []age.Recipient
	for _, arg := range keys {
		r, err := parseRecipient(arg)
		if err != nil {
			logFatalf("Error: %v", err)
		}
		recipients = append(recipients, r)
	}
	encrypt(recipients, in, out, armor)
}

func encryptPass(pass string, in io.Reader, out io.Writer, armor bool) {
	r, err := age.NewScryptRecipient(pass)
	if err != nil {
		logFatalf("Error: %v", err)
	}
	encrypt([]age.Recipient{r}, in, out, armor)
}

func encrypt(recipients []age.Recipient, in io.Reader, out io.Writer, armor bool) {
	ageEncrypt := age.Encrypt
	if armor {
		ageEncrypt = age.EncryptWithArmor
	}
	w, err := ageEncrypt(out, recipients...)
	if err != nil {
		logFatalf("Error: %v", err)
	}
	if _, err := io.Copy(w, in); err != nil {
		logFatalf("Error: %v", err)
	}
	if err := w.Close(); err != nil {
		logFatalf("Error: %v", err)
	}
}

func decrypt(keys []string, in io.Reader, out io.Writer) {
	identities := []age.Identity{
		// If there is an scrypt recipient (it will have to be the only one and)
		// this identity will be invoked.
		&LazyScryptIdentity{passphrasePrompt},
	}

	// TODO: use the default location if no arguments are provided:
	// os.UserConfigDir()/age/keys.txt, ~/.ssh/id_rsa, ~/.ssh/id_ed25519
	for _, name := range keys {
		ids, err := parseIdentitiesFile(name)
		if err != nil {
			logFatalf("Error: %v", err)
		}
		identities = append(identities, ids...)
	}

	r, err := age.Decrypt(in, identities...)
	if err != nil {
		logFatalf("Error: %v", err)
	}
	if _, err := io.Copy(out, r); err != nil {
		logFatalf("Error: %v", err)
	}
}

func passphrasePrompt() (string, error) {
	fmt.Fprintf(os.Stderr, "Enter passphrase: ")
	pass, err := readPassphrase()
	if err != nil {
		return "", fmt.Errorf("could not read passphrase: %v", err)
	}
	return string(pass), nil
}

func logFatalf(format string, v ...interface{}) {
	_log.Printf(format, v...)
	_log.Fatalf("[ Did age not do what you expected? Could an error be more useful?" +
		" Tell us: https://filippo.io/age/report ]")
}
