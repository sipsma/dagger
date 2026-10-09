package server

import "github.com/dagger/dagger/dagql"

// A side (#14578 head): settle takes no final flag, and a replay stops at its
// root's claim.

func benchSettleDelivered(sess *daggerSession, dgst string, targets []string) {
	sess.settleCallPayload(dgst, targets, true)
}

func benchReplay(store *callPayloadDeliveryStore, dgst string) {
	var keys dagql.CallPayloadSeenKeyStore = store
	keys.ClaimCallPayload(dgst)
}
