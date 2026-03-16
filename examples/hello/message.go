//go:generate go run ../../cmd/amgen --in=./message.go --out=./message.rs

package hello

type HelloRequest struct {
	Who      string `am:"who"`
	Question string `am:"question"`
}

type HelloReply struct {
	Answer   string        `am:"answer"`
	Internal HelloInternal `am:"internal"`
}

type HelloInternal struct {
	TraceID string `am:"trace_id"`
}
