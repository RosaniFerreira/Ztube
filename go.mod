module ztube

go 1.22

require (
	github.com/harry1453/go-common-file-dialog v1.2.6
	github.com/jchv/go-webview2 v0.0.0-20250406165304-0bcfea011047
	golang.org/x/sys v0.25.0
)

require (
	github.com/go-ole/go-ole v1.3.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jchv/go-winloader v0.0.0-20250406163304-c1995be93bd1 // indirect
)

replace (
	github.com/go-ole/go-ole => ./third_party/go-ole
	github.com/google/uuid => ./third_party/uuid
	github.com/harry1453/go-common-file-dialog => ./third_party/go-common-file-dialog
	github.com/jchv/go-webview2 => ./third_party/go-webview2
	github.com/jchv/go-winloader => ./third_party/go-winloader
	golang.org/x/sys => ./third_party/sys
)
