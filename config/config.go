package config

import "time"

const (
	ListenAddr       = ":8080"
	DBPath           = "./renia.db"
	AIEndpoint       = "http://127.0.0.1:8080/v1/chat/completions"
	AITimeout        = 45 * time.Second
	HistoryLimit     = 50
	PBKDF2Iterations = 600000
	SaltLength       = 32
	KeyLength        = 32
)
