/*
 * Copyright 2018 The CovenantSQL Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package mux

import (
	"net"
	"sync"

	"github.com/pkg/errors"
	mux "github.com/xtaci/smux"

	"github.com/CovenantSQL/CovenantSQL/conf"
	"github.com/CovenantSQL/CovenantSQL/proto"
	rrpc "github.com/CovenantSQL/CovenantSQL/rpc"
)

// Session is the Session type of SessionPool.
type Session struct {
	sync.RWMutex
	target proto.NodeID
	sess   []*mux.Session
	offset int
}

// SessionPool is the struct type of session pool.
type SessionPool struct {
	sync.RWMutex
	sessions map[proto.NodeID]*Session
}

var (
	DefaultPool = &SessionPool{
		sessions: make(map[proto.NodeID]*Session),
	}
)

// GetSessionPoolInstance return default SessionPool instance with rpc.DefaultDialer.
func GetSessionPoolInstance() *SessionPool {
	return DefaultPool
}

// Close closes the session.
func (s *Session) Close() {
	s.Lock()
	defer s.Unlock()
	for _, s := range s.sess {
		_ = s.Close()
	}
	s.sess = s.sess[:0]
}

// Get returns new connection from session.
func (s *Session) Get() (conn net.Conn, err error) {
	s.Lock()
	defer s.Unlock()
	s.offset++
	s.offset %= conf.MaxRPCPoolPhysicalConnection

	var (
		sess     *mux.Session
		stream   *mux.Stream
		sessions []*mux.Session
	)

	for {
		if len(s.sess) <= s.offset {
			// open new session
			sess, err = s.newSession()
			if err != nil {
				return
			}
			s.sess = append(s.sess, sess)
			s.offset = len(s.sess) - 1
		} else {
			sess = s.sess[s.offset]
		}

		// open connection
		stream, err = sess.OpenStream()
		if err != nil {
			// invalidate session
			sessions = nil
			sessions = append(sessions, s.sess[0:s.offset]...)
			sessions = append(sessions, s.sess[s.offset+1:]...)
			s.sess = sessions
			continue
		}

		conn = stream
		return
	}
}

// Len returns physical connection count.
func (s *Session) Len() int {
	s.RLock()
	defer s.RUnlock()
	return len(s.sess)
}

func newSession(id proto.NodeID, isAnonymous bool) (sess *mux.Session, err error) {
	var conn net.Conn
	if conn, err = rrpc.DialEx(id, isAnonymous); err != nil {
		err = errors.Wrap(err, "dialing new session connection failed")
		return
	}
	return mux.Client(conn, mux.DefaultConfig())
}

func (s *Session) newSession() (sess *mux.Session, err error) {
	return newSession(s.target, false)
}

func (p *SessionPool) getSession(id proto.NodeID) (sess *Session, loaded bool) {
	// NO Blocking operation in this function
	p.Lock()
	defer p.Unlock()
	sess, exist := p.sessions[id]
	if exist {
		//log.WithField("node", id).Debug("load session for target node")
		loaded = true
	} else {
		// new session
		sess = &Session{
			target: id,
		}
		p.sessions[id] = sess
	}
	return
}

// Get returns existing session to the node, if not exist try best to create one.
func (p *SessionPool) Get(id proto.NodeID) (conn net.Conn, err error) {
	var sess *Session
	sess, _ = p.getSession(id)
	return sess.Get()
}

type anonymousMuxConn struct {
	*mux.Stream
	sess *mux.Session
}

func (c *anonymousMuxConn) Close() error {
	if err := c.Stream.Close(); err != nil {
		return err
	}
	return c.sess.Close()
}

func (p *SessionPool) GetEx(id proto.NodeID, isAnonymous bool) (conn net.Conn, err error) {
	if isAnonymous {
		var (
			sess   *mux.Session
			stream *mux.Stream
		)
		if sess, err = newSession(id, true); err != nil {
			return
		}
		if stream, err = sess.OpenStream(); err != nil {
			err = errors.Wrapf(err, "open new session to %s failed", id)
			return
		}
		return &anonymousMuxConn{
			sess:   sess,
			Stream: stream,
		}, nil
	}
	return p.Get(id)
}

// Remove the node sessions in the pool.
func (p *SessionPool) Remove(id proto.NodeID) {
	p.Lock()
	defer p.Unlock()
	sess, exist := p.sessions[id]
	if exist {
		sess.Close()
		delete(p.sessions, id)
	}
	return
}

// Close closes all sessions in the pool.
func (p *SessionPool) Close() {
	p.Lock()
	defer p.Unlock()
	for _, s := range p.sessions {
		s.Close()
	}
	p.sessions = make(map[proto.NodeID]*Session)
}

// Len returns the session counts in the pool.
func (p *SessionPool) Len() (total int) {
	p.RLock()
	defer p.RUnlock()

	for _, s := range p.sessions {
		total += s.Len()
	}
	return
}
