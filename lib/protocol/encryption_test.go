// Copyright (C) 2019 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package protocol

import (
	"bytes"
	"fmt"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/syncthing/syncthing/lib/build"
	"github.com/syncthing/syncthing/lib/rand"
)

var (
	testKeyGen = NewKeyGenerator()

	// https://github.com/syncthing/syncthing/issues/8799
	cryptoIsBrokenUnderRaceDetector = (build.IsLinux || build.IsDarwin) && strings.HasPrefix(runtime.Version(), "go1.20")
)

func TestEnDecryptName(t *testing.T) {
	if cryptoIsBrokenUnderRaceDetector {
		t.Skip("cannot test")
	}

	pattern := regexp.MustCompile(
		fmt.Sprintf("^[0-9A-V]%s/[0-9A-V]{2}/([0-9A-V]{%d}/)*[0-9A-V]{1,%d}$",
			regexp.QuoteMeta(encryptedDirExtension),
			maxPathComponent, maxPathComponent-1))

	makeName := func(n int) string {
		b := make([]byte, n)
		for i := range b {
			b[i] = byte('a' + i%26)
		}
		return string(b)
	}

	var key [32]byte
	cases := []string{
		"",
		"foo",
		"a longer name/with/slashes and spaces",
		makeName(maxPathComponent),
		makeName(1 + maxPathComponent),
		makeName(2 * maxPathComponent),
		makeName(1 + 2*maxPathComponent),
	}
	for _, tc := range cases {
		var prev string
		for i := 0; i < 5; i++ {
			enc := encryptName(tc, &key)
			if prev != "" && prev != enc {
				t.Error("name should always encrypt the same")
			}
			prev = enc
			if tc != "" && strings.Contains(enc, tc) {
				t.Error("shouldn't contain plaintext")
			}
			if !pattern.MatchString(enc) {
				t.Fatalf("encrypted name %s doesn't match %s",
					enc, pattern)
			}

			dec, err := decryptName(enc, &key)
			if err != nil {
				t.Error(err)
			}
			if dec != tc {
				t.Error("mismatch after decryption")
			}
			t.Logf("%q encrypts as %q", tc, enc)
		}
	}
}

func TestKeyDerivation(t *testing.T) {
	folderKey := testKeyGen.KeyFromPassword("my folder", "my password")
	encryptedName := encryptDeterministic([]byte("filename.txt"), folderKey, nil)
	if base32Hex.EncodeToString(encryptedName) != "3T5957I4IOA20VEIEER6JSQG0PEPIRV862II3K7LOF75Q" {
		t.Error("encrypted name mismatch")
	}

	fileKey := testKeyGen.FileKey("filename.txt", folderKey)
	// fmt.Println(base32Hex.EncodeToString(encryptBytes([]byte("hello world"), fileKey))) => A1IPD...
	const encrypted = `A1IPD28ISL7VNPRSSSQM2L31L3IJPC08283RO89J5UG0TI9P38DO9RFGK12DK0KD7PKQP6U51UL2B6H96O`
	bs, _ := base32Hex.DecodeString(encrypted)
	dec, err := DecryptBytes(bs, fileKey)
	if err != nil {
		t.Error(err)
	}
	if string(dec) != "hello world" {
		t.Error("decryption mismatch")
	}
}

func TestDecryptNameInvalid(t *testing.T) {
	key := new([32]byte)
	for _, c := range []string{
		"T.syncthing-enc/OD",
		"T.syncthing-enc/OD/",
		"T.wrong-extension/OD/PHVDD67S7FI2K5QQMPSOFSK",
		"OD/PHVDD67S7FI2K5QQMPSOFSK",
	} {
		if _, err := decryptName(c, key); err == nil {
			t.Errorf("no error for %q", c)
		}
	}
}

func TestEnDecryptBytes(t *testing.T) {
	var key [32]byte
	cases := [][]byte{
		{},
		{1, 2, 3, 4, 5},
	}
	for _, tc := range cases {
		var prev []byte
		for i := 0; i < 5; i++ {
			enc := encryptBytes(tc, &key)
			if bytes.Equal(enc, prev) {
				t.Error("encryption should not repeat")
			}
			prev = enc
			if len(tc) > 0 && bytes.Contains(enc, tc) {
				t.Error("shouldn't contain plaintext")
			}
			dec, err := DecryptBytes(enc, &key)
			if err != nil {
				t.Error(err)
			}
			if !bytes.Equal(dec, tc) {
				t.Error("mismatch after decryption")
			}
		}
	}
}

func encFileInfo() FileInfo {
	return FileInfo{
		Name:        "hello",
		Size:        45,
		Permissions: 0o755,
		ModifiedS:   8080,
		Sequence:    1000,
		Blocks: []BlockInfo{
			{
				Offset: 0,
				Size:   45,
				Hash:   []byte{1, 2, 3},
			},
			{
				Offset: 45,
				Size:   45,
				Hash:   []byte{1, 2, 3},
			},
		},
	}
}

func TestEnDecryptFileInfo(t *testing.T) {
	if cryptoIsBrokenUnderRaceDetector {
		t.Skip("cannot test")
	}

	var key [32]byte
	fi := encFileInfo()

	enc := encryptFileInfo(testKeyGen, fi, &key)
	if bytes.Equal(enc.Blocks[0].Hash, enc.Blocks[1].Hash) {
		t.Error("block hashes should not repeat when on different offsets")
	}
	if enc.RawBlockSize < MinBlockSize {
		t.Error("Too small raw block size:", enc.RawBlockSize)
	}
	if enc.Sequence != fi.Sequence {
		t.Error("encrypted fileinfo didn't maintain sequence number")
	}
	again := encryptFileInfo(testKeyGen, fi, &key)
	if !bytes.Equal(enc.Blocks[0].Hash, again.Blocks[0].Hash) {
		t.Error("block hashes should remain stable (0)")
	}
	if !bytes.Equal(enc.Blocks[1].Hash, again.Blocks[1].Hash) {
		t.Error("block hashes should remain stable (1)")
	}

	// Simulate the remote setting the sequence number when writing to db
	enc.Sequence = 10

	dec, err := DecryptFileInfo(testKeyGen, enc, &key)
	if err != nil {
		t.Error(err)
	}
	if dec.Sequence != enc.Sequence {
		t.Error("decrypted fileinfo didn't maintain sequence number")
	}
	dec.Sequence = fi.Sequence
	if !reflect.DeepEqual(fi, dec) {
		t.Error("mismatch after decryption")
	}
}

func TestEncryptedFileInfoConsistency(t *testing.T) {
	if cryptoIsBrokenUnderRaceDetector {
		t.Skip("cannot test")
	}

	var key [32]byte
	files := []FileInfo{
		encFileInfo(),
		encFileInfo(),
	}
	files[1].SetIgnored()
	for i, f := range files {
		enc := encryptFileInfo(testKeyGen, f, &key)
		if err := checkFileInfoConsistency(enc); err != nil {
			t.Errorf("%v: %v", i, err)
		}
	}
}

func TestIsEncryptedParent(t *testing.T) {
	comp := rand.String(maxPathComponent)
	cases := []struct {
		path string
		is   bool
	}{
		{"", false},
		{".", false},
		{"/", false},
		{"12" + encryptedDirExtension, false},
		{"1" + encryptedDirExtension, true},
		{"1" + encryptedDirExtension + "/b", false},
		{"1" + encryptedDirExtension + "/bc", true},
		{"1" + encryptedDirExtension + "/bcd", false},
		{"1" + encryptedDirExtension + "/bc/foo", false},
		{"1.12/22", false},
		{"1" + encryptedDirExtension + "/bc/" + comp, true},
		{"1" + encryptedDirExtension + "/bc/" + comp + "/" + comp, true},
		{"1" + encryptedDirExtension + "/bc/" + comp + "a", false},
		{"1" + encryptedDirExtension + "/bc/" + comp + "/a/" + comp, false},
	}
	for _, tc := range cases {
		if res := IsEncryptedParent(strings.Split(tc.path, "/")); res != tc.is {
			t.Errorf("%v: got %v, expected %v", tc.path, res, tc.is)
		}
	}
}

func TestDeterministicEncryptionDecryption(t *testing.T) {
	t.Parallel()
	var key [keySize]byte
	copy(key[:], []byte("thisisatestkeyfortestingpurposesonly!!")) // 32-byte key

	testCases := []struct {
		name          string
		data          []byte
		additional    []byte
		expectedError bool
	}{
		{
			name:          "Empty data, no additional data",
			data:          []byte{},
			additional:    nil,
			expectedError: false,
		},
		{
			name:          "Regular string, no additional data",
			data:          []byte("Hello, world!"),
			additional:    nil,
			expectedError: false,
		},
		{
			name:          "Regular string with additional data",
			data:          []byte("Hello, world!"),
			additional:    []byte("extra"),
			expectedError: false,
		},
		{
			name:          "Large data block",
			data:          []byte(strings.Repeat("a", 1024)), // 1KB of 'a'
			additional:    nil,
			expectedError: false,
		},
		{
			name:          "Large data block with additional data",
			data:          []byte(strings.Repeat("a", 1024)), // 1KB of 'a'
			additional:    []byte("extra"),
			expectedError: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			enc1 := encryptDeterministic(tc.data, &key, tc.additional)
			enc2 := encryptDeterministic(tc.data, &key, tc.additional)

			if !bytes.Equal(enc1, enc2) {
				t.Errorf("Expected consistent encryption output, got different results")
			}

			dec, err := decryptDeterministic(enc1, &key, tc.additional)
			if tc.expectedError {
				if err == nil {
					t.Error("Expected an error, but got none")
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error during decryption: %v", err)
				}
				if !bytes.Equal(tc.data, dec) {
					t.Errorf("Decrypted data does not match original; got %q, want %q", dec, tc.data)
				}
			}

			if tc.additional != nil {
				differentEnc := encryptDeterministic(tc.data, &key, []byte("different"))
				if bytes.Equal(enc1, differentEnc) {
					t.Error("Changing additional data should yield different encryption output")
				}
			}
		})
	}
}

func TestRandomNonceUniqueness(t *testing.T) {
	t.Parallel()
	nonces := make(map[string]struct{})
	for i := 0; i < 100; i++ {
		nonce := randomNonce()
		nonceStr := string(nonce[:])
		if _, exists := nonces[nonceStr]; exists {
			t.Error("randomNonce generated a duplicate nonce")
		}
		nonces[nonceStr] = struct{}{}
	}
}

func TestDeslashify(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{
			name:    "Valid encrypted path with single character prefix",
			input:   "A.syncthing-enc/BC/DEFG",
			want:    "ABCDEFG",
			wantErr: false,
		},
		{
			name:    "Valid encrypted path with multiple components",
			input:   "T.syncthing-enc/UV/WXYZ/1234",
			want:    "TUVWXYZ1234",
			wantErr: false,
		},
		{
			name:    "Empty input string",
			input:   "",
			want:    "",
			wantErr: true,
		},
		{
			name:    "Invalid path missing encryptedDirExtension",
			input:   "A/randomdir/BC/DEFG",
			want:    "",
			wantErr: true,
		},
		{
			name:    "Valid path with only prefix and no components",
			input:   "X.syncthing-enc/",
			want:    "X",
			wantErr: false,
		},
		{
			name:    "Path missing prefix",
			input:   "/syncthing-enc/DE/FG",
			want:    "",
			wantErr: true,
		},
		{
			name:    "Path with valid prefix but wrong extension",
			input:   "A.invalid-enc/BC/DEFG",
			want:    "",
			wantErr: true,
		},
		{
			name:    "Valid path with multiple separators",
			input:   "M.syncthing-enc/N/OP/QRS/TUV/WXYZ",
			want:    "MNOPQRSTUVWXYZ",
			wantErr: false,
		},
		{
			name:    "Path with only prefix and extension",
			input:   "K.syncthing-enc",
			want:    "K",
			wantErr: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := deslashify(test.input)
			if test.wantErr && err == nil {
				t.Errorf("Expected error but got nil")
			}
			if !test.wantErr && err != nil {
				t.Errorf("Got unexpected error: %v", err)
			}
			if got != test.want {
				t.Errorf("Expected %q, got %q", test.want, got)
			}
		})
	}
}
