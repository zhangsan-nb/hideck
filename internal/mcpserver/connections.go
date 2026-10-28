package mcpserver

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
)

type AudioCredentials struct{ Owner, MediaID, Lease string }
type Connection struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	TokenID     string `json:"token_id,omitempty"`
	Status      string `json:"status"`
	credentials AudioCredentials
}

type Connections struct {
	mu       sync.Mutex
	entries  map[string]Connection
	validate func(AudioCredentials) error
}

func NewConnections(validate func(AudioCredentials) error) *Connections {
	return &Connections{entries: make(map[string]Connection), validate: validate}
}
func (s *Connections) Bind(connection Connection, credentials AudioCredentials) (Connection, error) {
	if err := s.validate(credentials); err != nil {
		return Connection{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if connection.ID != "" {
		previous, ok := s.entries[connection.ID]
		if !ok || previous.credentials.Owner != credentials.Owner || previous.TokenID != connection.TokenID {
			return Connection{}, errors.New("音频连接不存在或不属于当前网页用户和令牌")
		}
	} else {
		random := make([]byte, 16)
		if _, err := rand.Read(random); err != nil {
			return Connection{}, err
		}
		connection.ID = hex.EncodeToString(random)
	}
	connection.credentials, connection.Status = credentials, "connected"
	s.entries[connection.ID] = connection
	return connection, nil
}
func (s *Connections) Resolve(tokenID, id string) (AudioCredentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	connection, ok := s.entries[id]
	if !ok || connection.TokenID != tokenID {
		return AudioCredentials{}, errors.New("需要在电话页连接 MCP 网页音频，并指定当前令牌的 audio_connection_id")
	}
	if err := s.validate(connection.credentials); err != nil {
		return AudioCredentials{}, errors.New("网页音频已失效，请在电话页重新连接 MCP：" + err.Error())
	}
	return connection.credentials, nil
}
func (s *Connections) List(tokenID string) []Connection {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := []Connection{}
	for _, connection := range s.entries {
		if err := s.validate(connection.credentials); err != nil {
			continue
		}
		if connection.TokenID != tokenID {
			continue
		}
		connection.TokenID = ""
		result = append(result, connection)
	}
	return result
}
func (s *Connections) Delete(id, owner string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	connection, ok := s.entries[id]
	if !ok {
		return nil
	}
	if connection.credentials.Owner != owner {
		return errors.New("音频连接不属于当前网页用户")
	}
	delete(s.entries, id)
	return nil
}
func (s *Connections) Revoke(tokenID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, connection := range s.entries {
		if connection.TokenID == tokenID {
			delete(s.entries, id)
		}
	}
}
func (s *Connections) Clear() { s.mu.Lock(); defer s.mu.Unlock(); clear(s.entries) }
