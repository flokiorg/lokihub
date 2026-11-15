package persist

type KVStore interface {
	Read(key string) ([]byte, error)
	Write(key string, data []byte) error
}

// Persister interface for saving/loading specific state
type Persister interface {
	// Add methods as needed for specific state components
}
