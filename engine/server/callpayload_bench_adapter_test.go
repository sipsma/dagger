package server

import "github.com/dagger/dagger/dagql"

// B side (repair): settle takes the final flag, and a replay whose root claim
// fails asks the store whether to repair, as core's repairCallPayloads does.

func benchSettleDelivered(sess *daggerSession, dgst string, targets []string) {
	sess.settleCallPayload(dgst, targets, true, false)
}

func benchReplay(store *callPayloadDeliveryStore, dgst string) {
	var keys dagql.CallPayloadSeenKeyStore = store
	if !keys.ClaimCallPayload(dgst) {
		if closures, ok := keys.(dagql.CallPayloadClosureStore); ok {
			closures.StartCallPayloadRepair(dgst)
		}
	}
}
