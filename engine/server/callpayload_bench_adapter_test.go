package server

import "github.com/dagger/dagger/dagql"

// B side (repair): settle takes the final flag, and a replay claims its root
// through ClaimCallPayloadRoot, which also decides whether to repair, as
// core's recordCallPayloads does.

func benchSettleDelivered(sess *daggerSession, dgst string, targets []string) {
	sess.settleCallPayload(dgst, targets, true, false)
}

func benchReplay(store *callPayloadDeliveryStore, dgst string) {
	var keys dagql.CallPayloadSeenKeyStore = store
	keys.ClaimCallPayloadRoot(dgst)
}
