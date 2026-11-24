//! Pure-Rust TLS 1.3 Reality client transport.

use std::{
    io,
    pin::Pin,
    task::{Context, Poll},
    time::{SystemTime, UNIX_EPOCH},
};

use aes_gcm::{
    aead::{Aead, KeyInit, Payload},
    Aes128Gcm, Aes256Gcm, Nonce,
};
use base64::{engine::general_purpose::URL_SAFE_NO_PAD, Engine as _};
use chacha20poly1305::ChaCha20Poly1305;
use hkdf::{
    hmac::{Hmac, Mac},
    Hkdf,
};
use rand::RngCore;
use sha2::{Digest, Sha256};
use tokio::io::{AsyncRead, AsyncReadExt, AsyncWrite, AsyncWriteExt, ReadBuf};
use x25519_dalek::{PublicKey, StaticSecret};

use crate::dialer::BoxedStream;

#[derive(Clone, Debug)]
pub struct RealityConfig {
    pub server_name: String,
    pub public_key: [u8; 32],
    pub short_id: Vec<u8>,
    pub alpn: Vec<Vec<u8>>,
}

impl RealityConfig {
    pub fn from_strings(
        server_name: impl Into<String>,
        public_key_b64: &str,
        short_id_hex: &str,
    ) -> io::Result<Self> {
        let pbk_bytes = URL_SAFE_NO_PAD
            .decode(public_key_b64.trim().trim_end_matches('='))
            .map_err(|e| io::Error::new(io::ErrorKind::InvalidInput, format!("invalid pbk: {e}")))?;
        let public_key: [u8; 32] = pbk_bytes.try_into().map_err(|_| {
            io::Error::new(io::ErrorKind::InvalidInput, "Reality pbk must be 32 bytes")
        })?;
        let short_id = decode_hex(short_id_hex.trim())?;
        if short_id.len() > 8 {
            return Err(io::Error::new(
                io::ErrorKind::InvalidInput,
                "Reality short_id must be <= 8 bytes",
            ));
        }
        Ok(Self {
            server_name: server_name.into(),
            public_key,
            short_id,
            alpn: Vec::new(),
        })
    }
}

fn decode_hex(s: &str) -> io::Result<Vec<u8>> {
    if !s.len().is_multiple_of(2) {
        return Err(io::Error::new(io::ErrorKind::InvalidInput, "odd hex length"));
    }
    let mut out = Vec::with_capacity(s.len() / 2);
    for i in (0..s.len()).step_by(2) {
        let b = u8::from_str_radix(&s[i..i + 2], 16)
            .map_err(|_| io::Error::new(io::ErrorKind::InvalidInput, "invalid hex"))?;
        out.push(b);
    }
    Ok(out)
}

/// Derives the 16-byte Reality `auth_key` from ECDH shared secret and ClientHello random[..20].
pub fn derive_reality_auth_key(shared_secret: &[u8; 32], client_random: &[u8; 32]) -> [u8; 16] {
    let hk = Hkdf::<Sha256>::new(Some(&client_random[..20]), shared_secret);
    let mut auth_key = [0u8; 16];
    let _ = hk.expand(b"REALITY", &mut auth_key);
    auth_key
}

/// Seals the 16-byte Reality metadata payload into a 32-byte TLS ClientHello `session_id`.
pub fn seal_reality_session_id(
    auth_key: &[u8; 16],
    client_random: &[u8; 32],
    short_id: &[u8],
    aad_handshake_msg: &[u8],
) -> io::Result<[u8; 32]> {
    let mut plain = [0u8; 16];
    plain[0] = 1;
    plain[1] = 8;
    plain[2] = 4;
    let ts = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_secs() as u32;
    plain[4..8].copy_from_slice(&ts.to_be_bytes());
    let copy_len = short_id.len().min(8);
    plain[8..8 + copy_len].copy_from_slice(&short_id[..copy_len]);

    let cipher = Aes128Gcm::new_from_slice(auth_key)
        .map_err(|_| io::Error::new(io::ErrorKind::InvalidInput, "invalid Reality auth key"))?;
    let nonce = Nonce::from_slice(&client_random[20..32]);
    let sealed = cipher
        .encrypt(
            nonce,
            Payload {
                msg: &plain,
                aad: aad_handshake_msg,
            },
        )
        .map_err(|_| io::Error::new(io::ErrorKind::InvalidData, "Reality seal failed"))?;
    sealed.try_into().map_err(|_| {
        io::Error::new(io::ErrorKind::InvalidData, "Reality session_id must be 32 bytes")
    })
}

fn build_client_hello(
    client_random: &[u8; 32],
    eph_pub: &[u8; 32],
    cfg: &RealityConfig,
    auth_key: &[u8; 16],
) -> io::Result<(Vec<u8>, Vec<u8>)> {
    let mut exts = Vec::new();
    // SNI (0x0000)
    if !cfg.server_name.is_empty() {
        let name = cfg.server_name.as_bytes();
        let list_len = 1 + 2 + name.len();
        exts.extend_from_slice(&0x0000u16.to_be_bytes());
        exts.extend_from_slice(&((2 + list_len) as u16).to_be_bytes());
        exts.extend_from_slice(&(list_len as u16).to_be_bytes());
        exts.push(0x00);
        exts.extend_from_slice(&(name.len() as u16).to_be_bytes());
        exts.extend_from_slice(name);
    }
    // supported_versions (0x002b): TLS 1.3 (0x0304)
    exts.extend_from_slice(&[0x00, 0x2b, 0x00, 0x03, 0x02, 0x03, 0x04]);
    // supported_groups (0x000a): X25519 (0x001d)
    exts.extend_from_slice(&[0x00, 0x0a, 0x00, 0x04, 0x00, 0x02, 0x00, 0x1d]);
    // signature_algorithms (0x000d)
    exts.extend_from_slice(&[
        0x00, 0x0d, 0x00, 0x0a, 0x00, 0x08, 0x04, 0x03, 0x08, 0x04, 0x08, 0x07, 0x04, 0x01,
    ]);
    // key_share (0x0033): X25519
    exts.extend_from_slice(&[0x00, 0x33, 0x00, 0x26, 0x00, 0x24, 0x00, 0x1d, 0x00, 0x20]);
    exts.extend_from_slice(eph_pub);
    // ALPN (0x0010)
    if !cfg.alpn.is_empty() {
        let mut alpn_list = Vec::new();
        for p in &cfg.alpn {
            alpn_list.push(p.len() as u8);
            alpn_list.extend_from_slice(p);
        }
        exts.extend_from_slice(&0x0010u16.to_be_bytes());
        exts.extend_from_slice(&((2 + alpn_list.len()) as u16).to_be_bytes());
        exts.extend_from_slice(&(alpn_list.len() as u16).to_be_bytes());
        exts.extend_from_slice(&alpn_list);
    }

    let mut body = Vec::with_capacity(128 + exts.len());
    body.extend_from_slice(&[0x03, 0x03]); // legacy_version TLS 1.2
    body.extend_from_slice(client_random);
    let sid_offset = body.len() + 1;
    body.push(32); // session_id len
    body.extend_from_slice(&[0u8; 32]);
    // cipher suites: TLS_AES_128_GCM_SHA256, TLS_CHACHA20_POLY1305_SHA256, TLS_AES_256_GCM_SHA384
    body.extend_from_slice(&[0x00, 0x06, 0x13, 0x01, 0x13, 0x03, 0x13, 0x02]);
    body.extend_from_slice(&[0x01, 0x00]); // compression_methods: null
    body.extend_from_slice(&(exts.len() as u16).to_be_bytes());
    body.extend_from_slice(&exts);

    let mut hs = Vec::with_capacity(4 + body.len());
    hs.push(0x01); // ClientHello
    let blen = body.len() as u32;
    hs.extend_from_slice(&blen.to_be_bytes()[1..4]);
    hs.extend_from_slice(&body);

    let sid = seal_reality_session_id(auth_key, client_random, &cfg.short_id, &hs)?;
    hs[4 + sid_offset..4 + sid_offset + 32].copy_from_slice(&sid);

    let mut record = Vec::with_capacity(5 + hs.len());
    record.extend_from_slice(&[0x16, 0x03, 0x01]);
    record.extend_from_slice(&(hs.len() as u16).to_be_bytes());
    record.extend_from_slice(&hs);
    Ok((record, hs))
}

#[derive(Clone, Copy)]
enum Tls13Cipher {
    Aes128Gcm,
    Aes256Gcm,
    ChaCha20Poly1305,
}

impl Tls13Cipher {
    fn from_u16(suite: u16) -> io::Result<Self> {
        match suite {
            0x1301 => Ok(Self::Aes128Gcm),
            0x1302 => Ok(Self::Aes256Gcm),
            0x1303 => Ok(Self::ChaCha20Poly1305),
            _ => Err(io::Error::new(
                io::ErrorKind::InvalidData,
                format!("unsupported TLS 1.3 cipher suite: {suite:#06x}"),
            )),
        }
    }

    fn key_len(self) -> usize {
        match self {
            Self::Aes128Gcm => 16,
            Self::Aes256Gcm | Self::ChaCha20Poly1305 => 32,
        }
    }
}

fn hkdf_expand_label(secret: &[u8], label: &[u8], context: &[u8], len: usize) -> Vec<u8> {
    let mut hkdf_label = Vec::with_capacity(2 + 1 + 6 + label.len() + 1 + context.len());
    hkdf_label.extend_from_slice(&(len as u16).to_be_bytes());
    let full_label_len = 6 + label.len();
    hkdf_label.push(full_label_len as u8);
    hkdf_label.extend_from_slice(b"tls13 ");
    hkdf_label.extend_from_slice(label);
    hkdf_label.push(context.len() as u8);
    hkdf_label.extend_from_slice(context);
    let hk = Hkdf::<Sha256>::from_prk(secret).expect("valid prk");
    let mut out = vec![0u8; len];
    let _ = hk.expand(&hkdf_label, &mut out);
    out
}

fn derive_secret(secret: &[u8], label: &[u8], transcript_hash: &[u8]) -> Vec<u8> {
    hkdf_expand_label(secret, label, transcript_hash, 32)
}

#[derive(Clone)]
struct TrafficKeys {
    cipher: Tls13Cipher,
    key: Vec<u8>,
    iv: [u8; 12],
    seq: u64,
}

impl TrafficKeys {
    fn from_secret(cipher: Tls13Cipher, secret: &[u8]) -> Self {
        let key = hkdf_expand_label(secret, b"key", &[], cipher.key_len());
        let iv_vec = hkdf_expand_label(secret, b"iv", &[], 12);
        let mut iv = [0u8; 12];
        iv.copy_from_slice(&iv_vec);
        Self {
            cipher,
            key,
            iv,
            seq: 0,
        }
    }

    fn next_nonce(&mut self) -> [u8; 12] {
        let mut nonce = self.iv;
        let seq_bytes = self.seq.to_be_bytes();
        for i in 0..8 {
            nonce[4 + i] ^= seq_bytes[i];
        }
        self.seq += 1;
        nonce
    }

    fn seal_record(&mut self, record_type: u8, payload: &[u8]) -> io::Result<Vec<u8>> {
        let mut inner = Vec::with_capacity(payload.len() + 1);
        inner.extend_from_slice(payload);
        inner.push(record_type);
        let total_len = (inner.len() + 16) as u16;
        let aad = [0x17, 0x03, 0x03, (total_len >> 8) as u8, total_len as u8];
        let nonce_bytes = self.next_nonce();
        let nonce = Nonce::from_slice(&nonce_bytes);
        let p = Payload {
            msg: &inner,
            aad: &aad,
        };
        let ct = match self.cipher {
            Tls13Cipher::Aes128Gcm => Aes128Gcm::new_from_slice(&self.key)
                .unwrap()
                .encrypt(nonce, p),
            Tls13Cipher::Aes256Gcm => Aes256Gcm::new_from_slice(&self.key)
                .unwrap()
                .encrypt(nonce, p),
            Tls13Cipher::ChaCha20Poly1305 => ChaCha20Poly1305::new_from_slice(&self.key)
                .unwrap()
                .encrypt(nonce, p),
        }
        .map_err(|_| io::Error::new(io::ErrorKind::InvalidData, "TLS 1.3 seal failed"))?;
        let mut out = Vec::with_capacity(5 + ct.len());
        out.extend_from_slice(&aad);
        out.extend_from_slice(&ct);
        Ok(out)
    }

    fn open_record(&mut self, hdr: &[u8; 5], ct: &[u8]) -> io::Result<(u8, Vec<u8>)> {
        let nonce_bytes = self.next_nonce();
        let nonce = Nonce::from_slice(&nonce_bytes);
        let p = Payload { msg: ct, aad: hdr };
        let mut plain = match self.cipher {
            Tls13Cipher::Aes128Gcm => Aes128Gcm::new_from_slice(&self.key)
                .unwrap()
                .decrypt(nonce, p),
            Tls13Cipher::Aes256Gcm => Aes256Gcm::new_from_slice(&self.key)
                .unwrap()
                .decrypt(nonce, p),
            Tls13Cipher::ChaCha20Poly1305 => ChaCha20Poly1305::new_from_slice(&self.key)
                .unwrap()
                .decrypt(nonce, p),
        }
        .map_err(|_| io::Error::new(io::ErrorKind::InvalidData, "TLS 1.3 open failed"))?;
        while plain.last() == Some(&0) {
            plain.pop();
        }
        let rec_type = plain.pop().ok_or_else(|| {
            io::Error::new(io::ErrorKind::InvalidData, "empty TLS 1.3 inner plaintext")
        })?;
        Ok((rec_type, plain))
    }
}

fn parse_server_hello(hs: &[u8]) -> io::Result<(Tls13Cipher, [u8; 32])> {
    if hs.len() < 4 + 34 || hs[0] != 0x02 {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            "expected ServerHello",
        ));
    }
    let body = &hs[4..];
    let sid_len = body[34] as usize;
    let mut pos = 35 + sid_len;
    if pos + 3 > body.len() {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            "truncated ServerHello",
        ));
    }
    let suite = u16::from_be_bytes([body[pos], body[pos + 1]]);
    let cipher = Tls13Cipher::from_u16(suite)?;
    pos += 3; // suite + compression
    if pos + 2 > body.len() {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            "missing ServerHello extensions",
        ));
    }
    let ext_len = u16::from_be_bytes([body[pos], body[pos + 1]]) as usize;
    pos += 2;
    let ext_end = (pos + ext_len).min(body.len());
    let mut server_pub = None;
    while pos + 4 <= ext_end {
        let kind = u16::from_be_bytes([body[pos], body[pos + 1]]);
        let len = u16::from_be_bytes([body[pos + 2], body[pos + 3]]) as usize;
        pos += 4;
        if pos + len > ext_end {
            break;
        }
        if kind == 0x0033 && len >= 4 + 32 {
            let group = u16::from_be_bytes([body[pos], body[pos + 1]]);
            let klen = u16::from_be_bytes([body[pos + 2], body[pos + 3]]) as usize;
            if group == 0x001d && klen == 32 && pos + 4 + 32 <= ext_end {
                let mut key = [0u8; 32];
                key.copy_from_slice(&body[pos + 4..pos + 36]);
                server_pub = Some(key);
            }
        }
        pos += len;
    }
    let pub_key = server_pub.ok_or_else(|| {
        io::Error::new(
            io::ErrorKind::InvalidData,
            "ServerHello missing X25519 key_share",
        )
    })?;
    Ok((cipher, pub_key))
}

async fn read_tls_record(stream: &mut BoxedStream) -> io::Result<([u8; 5], Vec<u8>)> {
    let mut hdr = [0u8; 5];
    stream.read_exact(&mut hdr).await?;
    let len = u16::from_be_bytes([hdr[3], hdr[4]]) as usize;
    let mut body = vec![0u8; len];
    stream.read_exact(&mut body).await?;
    Ok((hdr, body))
}

/// Performs a TLS 1.3 Reality client handshake over `stream` and returns an encrypted stream.
pub async fn connect_reality(
    mut stream: BoxedStream,
    cfg: &RealityConfig,
) -> io::Result<BoxedStream> {
    let mut client_random = [0u8; 32];
    rand::thread_rng().fill_bytes(&mut client_random);

    let eph_priv = StaticSecret::random_from_rng(rand::thread_rng());
    let eph_pub = PublicKey::from(&eph_priv);
    let server_pbk = PublicKey::from(cfg.public_key);
    let auth_shared = eph_priv.diffie_hellman(&server_pbk);
    let auth_key = derive_reality_auth_key(auth_shared.as_bytes(), &client_random);

    let (ch_record, ch_hs) =
        build_client_hello(&client_random, eph_pub.as_bytes(), cfg, &auth_key)?;
    stream.write_all(&ch_record).await?;
    stream.flush().await?;

    let mut transcript = Sha256::new();
    transcript.update(&ch_hs);

    // Read ServerHello (skip optional ChangeCipherSpec if present)
    let (_sh_hdr, sh_rec) = loop {
        let (hdr, rec) = read_tls_record(&mut stream).await?;
        if hdr[0] == 0x14 {
            continue;
        }
        break (hdr, rec);
    };
    let (cipher, server_pub_bytes) = parse_server_hello(&sh_rec)?;
    transcript.update(&sh_rec);
    let hello_hash: [u8; 32] = transcript.clone().finalize().into();

    let tls_shared = eph_priv.diffie_hellman(&PublicKey::from(server_pub_bytes));
    let zeros = [0u8; 32];
    let (early_secret, _) = Hkdf::<Sha256>::extract(Some(&zeros), &zeros);
    let derived = derive_secret(&early_secret, b"derived", &Sha256::digest([]));
    let (handshake_secret, _) = Hkdf::<Sha256>::extract(Some(&derived), tls_shared.as_bytes());
    let c_hs_secret = derive_secret(&handshake_secret, b"c hs traffic", &hello_hash);
    let s_hs_secret = derive_secret(&handshake_secret, b"s hs traffic", &hello_hash);

    let mut c_hs_keys = TrafficKeys::from_secret(cipher, &c_hs_secret);
    let mut s_hs_keys = TrafficKeys::from_secret(cipher, &s_hs_secret);

    // Read encrypted server handshake messages until Finished (0x14)
    let mut hs_buf = Vec::new();
    let mut server_finished_seen = false;
    while !server_finished_seen {
        let (hdr, rec) = read_tls_record(&mut stream).await?;
        if hdr[0] == 0x14 {
            continue;
        }
        if hdr[0] != 0x17 {
            return Err(io::Error::new(
                io::ErrorKind::InvalidData,
                "unexpected plaintext record during Reality handshake",
            ));
        }
        let (rec_type, plain) = s_hs_keys.open_record(&hdr, &rec)?;
        if rec_type != 0x16 {
            continue;
        }
        hs_buf.extend_from_slice(&plain);
        while hs_buf.len() >= 4 {
            let msg_len =
                ((hs_buf[1] as usize) << 16) | ((hs_buf[2] as usize) << 8) | (hs_buf[3] as usize);
            if hs_buf.len() < 4 + msg_len {
                break;
            }
            let msg: Vec<u8> = hs_buf.drain(..4 + msg_len).collect();
            if msg[0] == 0x14 {
                // Verify Server Finished
                let pre_hash: [u8; 32] = transcript.clone().finalize().into();
                let finished_key = hkdf_expand_label(&s_hs_secret, b"finished", &[], 32);
                let mut mac =
                    <Hmac<Sha256> as Mac>::new_from_slice(&finished_key).expect("hmac key");
                mac.update(&pre_hash);
                if mac.verify_slice(&msg[4..]).is_err() {
                    return Err(io::Error::new(
                        io::ErrorKind::InvalidData,
                        "Reality Server Finished MAC mismatch",
                    ));
                }
                transcript.update(&msg);
                server_finished_seen = true;
                break;
            } else {
                transcript.update(&msg);
            }
        }
    }

    let server_fin_hash: [u8; 32] = transcript.finalize().into();
    let derived2 = derive_secret(&handshake_secret, b"derived", &Sha256::digest([]));
    let (master_secret, _) = Hkdf::<Sha256>::extract(Some(&derived2), &zeros);
    let c_app_secret = derive_secret(&master_secret, b"c ap traffic", &server_fin_hash);
    let s_app_secret = derive_secret(&master_secret, b"s ap traffic", &server_fin_hash);

    // Send client ChangeCipherSpec + Finished
    let c_fin_key = hkdf_expand_label(&c_hs_secret, b"finished", &[], 32);
    let mut c_mac = <Hmac<Sha256> as Mac>::new_from_slice(&c_fin_key).expect("hmac key");
    c_mac.update(&server_fin_hash);
    let verify_data = c_mac.finalize().into_bytes();
    let mut fin_msg = Vec::with_capacity(4 + 32);
    fin_msg.extend_from_slice(&[0x14, 0x00, 0x00, 0x20]);
    fin_msg.extend_from_slice(&verify_data);
    let fin_rec = c_hs_keys.seal_record(0x16, &fin_msg)?;

    let mut ccs_and_fin = Vec::with_capacity(6 + fin_rec.len());
    ccs_and_fin.extend_from_slice(&[0x14, 0x03, 0x03, 0x00, 0x01, 0x01]);
    ccs_and_fin.extend_from_slice(&fin_rec);
    stream.write_all(&ccs_and_fin).await?;
    stream.flush().await?;

    Ok(Box::new(RealityStream {
        inner: stream,
        read_keys: TrafficKeys::from_secret(cipher, &s_app_secret),
        write_keys: TrafficKeys::from_secret(cipher, &c_app_secret),
        read_hdr: [0u8; 5],
        read_hdr_filled: 0,
        read_ct: Vec::new(),
        read_ct_filled: 0,
        plain_buf: Vec::new(),
        plain_pos: 0,
        eof: false,
        write_buf: Vec::new(),
        write_pos: 0,
        pending_plain_len: 0,
    }) as BoxedStream)
}

struct RealityStream {
    inner: BoxedStream,
    read_keys: TrafficKeys,
    write_keys: TrafficKeys,
    read_hdr: [u8; 5],
    read_hdr_filled: usize,
    read_ct: Vec<u8>,
    read_ct_filled: usize,
    plain_buf: Vec<u8>,
    plain_pos: usize,
    eof: bool,
    write_buf: Vec<u8>,
    write_pos: usize,
    pending_plain_len: usize,
}

impl RealityStream {
    fn flush_write_buf(&mut self, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        while self.write_pos < self.write_buf.len() {
            match Pin::new(&mut self.inner).poll_write(cx, &self.write_buf[self.write_pos..]) {
                Poll::Ready(Ok(0)) => {
                    return Poll::Ready(Err(io::Error::new(
                        io::ErrorKind::WriteZero,
                        "Reality stream closed",
                    )));
                }
                Poll::Ready(Ok(n)) => self.write_pos += n,
                Poll::Ready(Err(e)) => return Poll::Ready(Err(e)),
                Poll::Pending => return Poll::Pending,
            }
        }
        self.write_buf.clear();
        self.write_pos = 0;
        Poll::Ready(Ok(()))
    }
}

impl AsyncRead for RealityStream {
    fn poll_read(
        mut self: Pin<&mut Self>,
        cx: &mut Context<'_>,
        out: &mut ReadBuf<'_>,
    ) -> Poll<io::Result<()>> {
        loop {
            if self.plain_pos < self.plain_buf.len() {
                let avail = &self.plain_buf[self.plain_pos..];
                let n = avail.len().min(out.remaining());
                out.put_slice(&avail[..n]);
                self.plain_pos += n;
                if self.plain_pos == self.plain_buf.len() {
                    self.plain_buf.clear();
                    self.plain_pos = 0;
                }
                return Poll::Ready(Ok(()));
            }
            if self.eof {
                return Poll::Ready(Ok(()));
            }
            let this = self.as_mut().get_mut();
            while this.read_hdr_filled < 5 {
                let mut rb = ReadBuf::new(&mut this.read_hdr[this.read_hdr_filled..]);
                match Pin::new(&mut this.inner).poll_read(cx, &mut rb) {
                    Poll::Ready(Ok(())) => {
                        let n = rb.filled().len();
                        if n == 0 {
                            this.eof = true;
                            return Poll::Ready(Ok(()));
                        }
                        this.read_hdr_filled += n;
                    }
                    Poll::Ready(Err(e)) => return Poll::Ready(Err(e)),
                    Poll::Pending => return Poll::Pending,
                }
            }
            let ct_len = u16::from_be_bytes([this.read_hdr[3], this.read_hdr[4]]) as usize;
            if this.read_ct.len() != ct_len {
                this.read_ct.resize(ct_len, 0);
            }
            while this.read_ct_filled < ct_len {
                let mut rb = ReadBuf::new(&mut this.read_ct[this.read_ct_filled..]);
                match Pin::new(&mut this.inner).poll_read(cx, &mut rb) {
                    Poll::Ready(Ok(())) => {
                        let n = rb.filled().len();
                        if n == 0 {
                            return Poll::Ready(Err(io::Error::new(
                                io::ErrorKind::UnexpectedEof,
                                "truncated TLS 1.3 record",
                            )));
                        }
                        this.read_ct_filled += n;
                    }
                    Poll::Ready(Err(e)) => return Poll::Ready(Err(e)),
                    Poll::Pending => return Poll::Pending,
                }
            }
            let hdr = this.read_hdr;
            let ct = std::mem::take(&mut this.read_ct);
            this.read_hdr_filled = 0;
            this.read_ct_filled = 0;
            if hdr[0] == 0x14 {
                continue;
            }
            let (rec_type, plain) = this.read_keys.open_record(&hdr, &ct)?;
            if rec_type == 0x17 {
                this.plain_buf = plain;
                this.plain_pos = 0;
            } else if rec_type == 0x15 && plain.get(1) == Some(&0) {
                this.eof = true;
                return Poll::Ready(Ok(()));
            }
        }
    }
}

impl AsyncWrite for RealityStream {
    fn poll_write(
        mut self: Pin<&mut Self>,
        cx: &mut Context<'_>,
        buf: &[u8],
    ) -> Poll<io::Result<usize>> {
        if self.write_pos < self.write_buf.len() {
            match self.as_mut().flush_write_buf(cx) {
                Poll::Ready(Ok(())) => {
                    let accepted = self.pending_plain_len;
                    self.pending_plain_len = 0;
                    if accepted > 0 {
                        return Poll::Ready(Ok(accepted));
                    }
                }
                Poll::Ready(Err(e)) => return Poll::Ready(Err(e)),
                Poll::Pending => return Poll::Pending,
            }
        }
        if buf.is_empty() {
            return Poll::Ready(Ok(0));
        }
        let take = buf.len().min(16384);
        let this = self.as_mut().get_mut();
        this.write_buf = this.write_keys.seal_record(0x17, &buf[..take])?;
        this.write_pos = 0;
        this.pending_plain_len = take;
        match self.as_mut().flush_write_buf(cx) {
            Poll::Ready(Ok(())) => {
                self.pending_plain_len = 0;
                Poll::Ready(Ok(take))
            }
            Poll::Ready(Err(e)) => Poll::Ready(Err(e)),
            Poll::Pending => Poll::Pending,
        }
    }

    fn poll_flush(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        match self.as_mut().flush_write_buf(cx) {
            Poll::Ready(Ok(())) => Pin::new(&mut self.inner).poll_flush(cx),
            other => other,
        }
    }

    fn poll_shutdown(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        if self.write_buf.is_empty() {
            if let Ok(alert) = self.write_keys.seal_record(0x15, &[1, 0]) {
                self.write_buf = alert;
                self.write_pos = 0;
            }
        }
        match self.as_mut().flush_write_buf(cx) {
            Poll::Ready(Ok(())) => Pin::new(&mut self.inner).poll_shutdown(cx),
            other => other,
        }
    }
}
