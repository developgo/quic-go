// This file was automatically generated by genny.
// Any changes will be lost if this file is regenerated.
// see https://github.com/cheekybits/genny

package quic

import (
	"fmt"
	"sync"

	"github.com/lucas-clemente/quic-go/internal/protocol"
	"github.com/lucas-clemente/quic-go/internal/qerr"
	"github.com/lucas-clemente/quic-go/internal/wire"
)

type outgoingBidiStreamsMap struct {
	mutex sync.RWMutex

	openQueue []chan struct{}

	streams map[protocol.StreamID]streamI

	nextStream  protocol.StreamID // stream ID of the stream returned by OpenStream(Sync)
	maxStream   protocol.StreamID // the maximum stream ID we're allowed to open
	blockedSent bool              // was a STREAMS_BLOCKED sent for the current maxStream

	newStream            func(protocol.StreamID) streamI
	queueStreamIDBlocked func(*wire.StreamsBlockedFrame)

	closeErr error
}

func newOutgoingBidiStreamsMap(
	nextStream protocol.StreamID,
	newStream func(protocol.StreamID) streamI,
	queueControlFrame func(wire.Frame),
) *outgoingBidiStreamsMap {
	return &outgoingBidiStreamsMap{
		streams:              make(map[protocol.StreamID]streamI),
		nextStream:           nextStream,
		maxStream:            protocol.InvalidStreamID,
		newStream:            newStream,
		queueStreamIDBlocked: func(f *wire.StreamsBlockedFrame) { queueControlFrame(f) },
	}
}

func (m *outgoingBidiStreamsMap) OpenStream() (streamI, error) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if m.closeErr != nil {
		return nil, m.closeErr
	}

	str, err := m.openStreamImpl()
	if err != nil {
		return nil, streamOpenErr{err}
	}
	return str, nil
}

func (m *outgoingBidiStreamsMap) OpenStreamSync() (streamI, error) {
	m.mutex.Lock()

	if m.closeErr != nil {
		m.mutex.Unlock()
		return nil, m.closeErr
	}
	str, err := m.openStreamImpl()
	if err == nil {
		m.mutex.Unlock()
		return str, nil
	}
	if err != errTooManyOpenStreams {
		m.mutex.Unlock()
		return nil, streamOpenErr{err}
	}
	waitChan := make(chan struct{})
	m.openQueue = append(m.openQueue, waitChan)
	m.mutex.Unlock()
	<-waitChan

	return m.OpenStream()
}

func (m *outgoingBidiStreamsMap) openStreamImpl() (streamI, error) {
	if m.nextStream > m.maxStream {
		if !m.blockedSent {
			var streamNum uint64
			if m.maxStream != protocol.InvalidStreamID {
				streamNum = m.maxStream.StreamNum()
			}
			m.queueStreamIDBlocked(&wire.StreamsBlockedFrame{
				Type:        protocol.StreamTypeBidi,
				StreamLimit: streamNum,
			})
			m.blockedSent = true
		}
		return nil, errTooManyOpenStreams
	}
	s := m.newStream(m.nextStream)
	m.streams[m.nextStream] = s
	m.nextStream += 4
	return s, nil
}

func (m *outgoingBidiStreamsMap) GetStream(id protocol.StreamID) (streamI, error) {
	m.mutex.RLock()
	if id >= m.nextStream {
		m.mutex.RUnlock()
		return nil, qerr.Error(qerr.StreamStateError, fmt.Sprintf("peer attempted to open stream %d", id))
	}
	s := m.streams[id]
	m.mutex.RUnlock()
	return s, nil
}

func (m *outgoingBidiStreamsMap) DeleteStream(id protocol.StreamID) error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if _, ok := m.streams[id]; !ok {
		return fmt.Errorf("Tried to delete unknown stream %d", id)
	}
	delete(m.streams, id)
	return nil
}

func (m *outgoingBidiStreamsMap) SetMaxStream(id protocol.StreamID) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if id <= m.maxStream {
		return
	}
	for i := m.maxStream.StreamNum(); i < id.StreamNum(); i++ {
		if len(m.openQueue) == 0 {
			break
		}
		close(m.openQueue[0])
		m.openQueue = m.openQueue[1:]
	}
	m.maxStream = id
	m.blockedSent = false
}

func (m *outgoingBidiStreamsMap) CloseWithError(err error) {
	m.mutex.Lock()
	m.closeErr = err
	for _, str := range m.streams {
		str.closeForShutdown(err)
	}
	for _, c := range m.openQueue {
		close(c)
	}
	m.mutex.Unlock()
}
