use std::io::{self, Read, Write};

#[derive(Clone, Copy)]
pub enum Transport<'a> {
    Http(Option<(&'a [u8], &'a [u8])>),
    Socks5(Option<(&'a [u8], &'a [u8])>),
}

impl Transport<'_> {
    fn connect(self, stream: &mut (impl Read + Write), host: &str, port: u16) -> io::Result<()> {
        match self {
            Self::Http(credentials) => {
                crate::http_connect::connect(stream, host, port, credentials)
            }
            Self::Socks5(credentials) => crate::socks5::connect(stream, host, port, credentials),
        }
    }
}

/// The stream is connected to the entry. Resolve subsequent hop names and the
/// destination at their respective proxies. Drop the stream if a hop fails.
pub fn connect<'a, S: Read + Write>(
    mut stream: S,
    mut entry: Transport<'a>,
    remaining: &[(&str, u16, Transport<'a>)],
    destination: (&str, u16),
) -> io::Result<S> {
    for &(host, port, next) in remaining {
        entry.connect(&mut stream, host, port)?;
        entry = next;
    }
    entry.connect(&mut stream, destination.0, destination.1)?;
    Ok(stream)
}
