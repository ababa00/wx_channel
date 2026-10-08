package Resource

import _ "embed"

//go:embed CertInstallDocument.html
var CertInstallDocument []byte

//go:embed nfapi/sys/tdi/amd64/netfilter2.sys
var TdiAmd64Netfilter2 []byte

//go:embed nfapi/sys/tdi/i386/netfilter2.sys
var TdiI386Netfilter2 []byte

//go:embed nfapi/sys/wfp/amd64/netfilter2.sys
var WfpAmd64Netfilter2 []byte

//go:embed nfapi/sys/wfp/i386/netfilter2.sys
var WfpI386Netfilter2 []byte

// The 32-bit nfapi.dll is excluded from this repository by the root .gitignore
// rule "*.dll" and is therefore unavailable at build time. It is also dead weight
// for our build: src/nfapi/Release.go only writes the 32-bit copy when the
// WindowsX64 runtime probe is false, which cannot happen in a GOARCH=amd64
// binary. Dropping the embed keeps the package compiling without shipping a
// fake binary. Re-add the //go:embed line if you ever build GOARCH=386.
var NfapiWin32Nfapi []byte

//go:embed nfapi/dll/x64/nfapi.dll
var NfapiX64Nfapi []byte
