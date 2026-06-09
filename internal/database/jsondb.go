package database

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

type DefaultType int

const (
	DefaultList DefaultType = iota
	DefaultMap
)

type JsonDB struct {
	mu          sync.RWMutex
	path        string
	defaultType DefaultType
	data        interface{}
}

func NewJsonDB(path string, dt DefaultType) *JsonDB {
	return &JsonDB{path: path, defaultType: dt}
}

func (db *JsonDB) Load() {
	db.mu.Lock()
	defer db.mu.Unlock()
	raw, err := os.ReadFile(db.path)
	if err != nil {
		db.data = db.defaultData()
		return
	}
	var parsed interface{}
	if json.Unmarshal(raw, &parsed) != nil {
		db.data = db.defaultData()
		return
	}
	db.data = parsed
}

func (db *JsonDB) Get() interface{} {
	db.mu.RLock()
	defer db.mu.RUnlock()
	if db.data == nil {
		return db.defaultData()
	}
	return db.data
}

func (db *JsonDB) GetList() []interface{} {
	d := db.Get()
	if l, ok := d.([]interface{}); ok {
		return l
	}
	return []interface{}{}
}

func (db *JsonDB) GetMap() map[string]interface{} {
	d := db.Get()
	if m, ok := d.(map[string]interface{}); ok {
		return m
	}
	return map[string]interface{}{}
}

func (db *JsonDB) Save(data interface{}) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.data = data
	return db.write()
}

func (db *JsonDB) write() error {
	os.MkdirAll(filepath.Dir(db.path), 0755)
	raw, err := json.MarshalIndent(db.data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(db.path, raw, 0644)
}

func (db *JsonDB) defaultData() interface{} {
	if db.defaultType == DefaultMap {
		return map[string]interface{}{}
	}
	return []interface{}{}
}
