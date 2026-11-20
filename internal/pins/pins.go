// Package pins checks that the files which build, run and test the connector
// use exactly the versions and digests in pins.json, at the repository root.
// The manifest is JSON beside the Dockerfile, compose.yaml and Makefile it
// governs, so the standard library reads it and the check is a plain go test.
package pins
