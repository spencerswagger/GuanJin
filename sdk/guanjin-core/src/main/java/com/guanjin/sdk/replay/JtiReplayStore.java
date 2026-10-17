package com.guanjin.sdk.replay;

public interface JtiReplayStore {

    void checkAndStore(String jti, long expEpochSeconds);
}
