package com.guanjin.sdk;

@FunctionalInterface
public interface PrincipalProvider {

    GuanjinPrincipal get();
}
