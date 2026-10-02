package appserver

import "sync"

// QueueSize is how many messages wait for an app to sync them; the firmware holds 16 in RAM.
const QueueSize = 256

// queue is a companion's offline queue: what arrived since the app last synced, oldest first.
type queue struct {
	mu   sync.Mutex
	msgs []Message
}

// push adds a message; a full queue gives up its oldest channel message first, as addToOfflineQueue does, and only then its oldest message.
func (q *queue) push(m Message) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.msgs) >= QueueSize {
		drop := 0
		for i, have := range q.msgs {
			if have.Kind != MessageContact {
				drop = i
				break
			}
		}
		q.msgs = append(q.msgs[:drop], q.msgs[drop+1:]...)
	}
	q.msgs = append(q.msgs, m)
}

func (q *queue) pop() (Message, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.msgs) == 0 {
		return Message{}, false
	}
	m := q.msgs[0]
	q.msgs = q.msgs[1:]
	return m, true
}

func (q *queue) len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.msgs)
}
