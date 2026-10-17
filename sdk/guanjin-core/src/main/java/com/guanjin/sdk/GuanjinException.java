package com.guanjin.sdk;

public class GuanjinException extends RuntimeException {

    public GuanjinException(String message) {
        super(message);
    }

    public GuanjinException(String message, Throwable cause) {
        super(message, cause);
    }
}
