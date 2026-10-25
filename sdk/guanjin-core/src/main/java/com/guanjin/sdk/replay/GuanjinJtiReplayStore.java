package com.guanjin.sdk.replay;

public interface GuanjinJtiReplayStore {

    void checkAndStore(String jti, long expEpochSeconds);
}
