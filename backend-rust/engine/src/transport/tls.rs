//! Async TLS client transport powered by `tokio-rustls` and `ring`.

use std::{
    io,
    sync::{Arc, Once},
};

use rustls::{
    client::danger::{HandshakeSignatureValid, ServerCertVerified, ServerCertVerifier},
    pki_types::{CertificateDer, ServerName, UnixTime},
    ClientConfig, DigitallySignedStruct, RootCertStore, SignatureScheme,
};
use tokio_rustls::TlsConnector;

use crate::dialer::BoxedStream;

static CRYPTO_INIT: Once = Once::new();

pub fn ensure_crypto_provider() {
    CRYPTO_INIT.call_once(|| {
        let _ = rustls::crypto::ring::default_provider().install_default();
    });
}

/// TLS client parameters for proxy transports.
#[derive(Clone, Debug, Default)]
pub struct TlsConfig {
    pub sni: Option<String>,
    pub alpn: Vec<Vec<u8>>,
    pub insecure: bool,
}

#[derive(Debug)]
struct InsecureCertVerifier;

impl ServerCertVerifier for InsecureCertVerifier {
    fn verify_server_cert(
        &self,
        _end_entity: &CertificateDer<'_>,
        _intermediates: &[CertificateDer<'_>],
        _server_name: &ServerName<'_>,
        _ocsp_response: &[u8],
        _now: UnixTime,
    ) -> Result<ServerCertVerified, rustls::Error> {
        Ok(ServerCertVerified::assertion())
    }

    fn verify_tls12_signature(
        &self,
        _message: &[u8],
        _cert: &CertificateDer<'_>,
        _dss: &DigitallySignedStruct,
    ) -> Result<HandshakeSignatureValid, rustls::Error> {
        Ok(HandshakeSignatureValid::assertion())
    }

    fn verify_tls13_signature(
        &self,
        _message: &[u8],
        _cert: &CertificateDer<'_>,
        _dss: &DigitallySignedStruct,
    ) -> Result<HandshakeSignatureValid, rustls::Error> {
        Ok(HandshakeSignatureValid::assertion())
    }

    fn supported_verify_schemes(&self) -> Vec<SignatureScheme> {
        vec![
            SignatureScheme::RSA_PKCS1_SHA256,
            SignatureScheme::RSA_PKCS1_SHA384,
            SignatureScheme::RSA_PKCS1_SHA512,
            SignatureScheme::ECDSA_NISTP256_SHA256,
            SignatureScheme::ECDSA_NISTP384_SHA384,
            SignatureScheme::ECDSA_NISTP521_SHA512,
            SignatureScheme::RSA_PSS_SHA256,
            SignatureScheme::RSA_PSS_SHA384,
            SignatureScheme::RSA_PSS_SHA512,
            SignatureScheme::ED25519,
        ]
    }
}

pub(crate) fn client_config(cfg: &TlsConfig) -> ClientConfig {
    ensure_crypto_provider();
    let mut client_cfg = if cfg.insecure {
        ClientConfig::builder()
            .dangerous()
            .with_custom_certificate_verifier(Arc::new(InsecureCertVerifier))
            .with_no_client_auth()
    } else {
        let mut roots = RootCertStore::empty();
        roots.extend(webpki_roots::TLS_SERVER_ROOTS.iter().cloned());
        ClientConfig::builder()
            .with_root_certificates(roots)
            .with_no_client_auth()
    };
    if !cfg.alpn.is_empty() {
        client_cfg.alpn_protocols = cfg.alpn.clone();
    }

    client_cfg
}

/// Wraps an existing stream in a TLS client session.
pub async fn connect_tls(
    stream: BoxedStream,
    server_host: &str,
    cfg: &TlsConfig,
) -> io::Result<BoxedStream> {
    let sni_host = cfg
        .sni
        .as_deref()
        .filter(|s| !s.is_empty())
        .unwrap_or(server_host)
        .to_string();
    let server_name = ServerName::try_from(sni_host.clone()).map_err(|_| {
        io::Error::new(
            io::ErrorKind::InvalidInput,
            format!("invalid TLS SNI/server name: {sni_host}"),
        )
    })?;

    let client_cfg = client_config(cfg);
    let connector = TlsConnector::from(Arc::new(client_cfg));
    let tls_stream = connector
        .connect(server_name, stream)
        .await
        .map_err(|err| io::Error::new(io::ErrorKind::ConnectionAborted, err))?;
    Ok(Box::new(tls_stream) as BoxedStream)
}
