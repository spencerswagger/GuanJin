package com.guanjin.sdk.agent;

public final class GuanjinReportResponse {

    public String snapshotId;
    public long storedVersion;

    public GuanjinReportResponse() {
    }

    public GuanjinReportResponse(String snapshotId, long storedVersion) {
        this.snapshotId = snapshotId;
        this.storedVersion = storedVersion;
    }
}
