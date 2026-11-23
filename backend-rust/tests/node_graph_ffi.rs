extern crate smartvpn_core as _;
use std::ptr;
extern "C" {
    fn smartvpn_node_graph_v1(
        op: usize,
        handle: usize,
        index: usize,
        data: *mut u8,
        len: usize,
    ) -> usize;
}
struct Store(usize);
impl Store {
    fn new() -> Self {
        Self(unsafe { smartvpn_node_graph_v1(0, 0, 0, ptr::null_mut(), 0) })
    }
    fn update(&self, op: usize, b: &[u8]) -> usize {
        unsafe { smartvpn_node_graph_v1(op, self.0, 0, b.as_ptr().cast_mut(), b.len()) }
    }
    fn read(&self, op: usize) -> Vec<u8> {
        let size = self.update(op, &[]);
        let mut out = vec![0; size];
        assert_eq!(
            unsafe { smartvpn_node_graph_v1(op, self.0, 0, out.as_mut_ptr(), size) },
            size
        );
        out
    }
}
impl Drop for Store {
    fn drop(&mut self) {
        self.update(4, &[]);
    }
}
fn field(b: &mut Vec<u8>, v: &[u8]) {
    b.extend_from_slice(&(v.len() as u64).to_le_bytes());
    b.extend_from_slice(v);
}
#[test]
fn ordered_selection_and_invalid_replacements() {
    let s = Store::new();
    assert_eq!(s.read(2), [0]);
    for row in [b"one", b"two", b"one"] {
        assert_eq!(s.update(3, row), 0);
    }
    let before = s.read(2);
    let mut indices = 1u64.to_le_bytes().to_vec();
    indices.extend_from_slice(&9u64.to_le_bytes());
    assert_eq!(s.update(10, &indices), usize::MAX);
    assert_eq!(s.read(2), before);
    assert_eq!(s.update(10, &1u64.to_le_bytes()), 0);
    let mut kept = vec![1];
    field(&mut kept, b"two");
    assert_eq!(s.read(2), kept);
    assert_eq!(s.update(10, &[]), 0);
    assert_eq!(s.read(2), [1]);
}
#[test]
fn rename_profile_preserves_unrelated_entries_and_active_chain() {
    let s = Store::new();
    let mut active = vec![];
    field(&mut active, b"x");
    active.extend([1, 0]);
    let mut graph = vec![];
    field(&mut graph, &active);
    graph.push(1);
    field(&mut graph, b"a");
    field(&mut graph, b"old");
    field(&mut graph, b"c");
    field(&mut graph, b"other");
    assert_eq!(s.update(6, &graph), 0);
    assert_eq!(s.update(12, &[]), 1);
    assert_eq!(s.update(6, &graph[..graph.len() - 1]), usize::MAX);
    assert_eq!(s.read(7), graph);
    let mut rename = vec![];
    field(&mut rename, b"a");
    field(&mut rename, b"b");
    rename.extend(b"new");
    assert_eq!(s.update(9, &rename), 0);
    let mut next = vec![];
    field(&mut next, &active);
    next.push(1);
    field(&mut next, b"b");
    field(&mut next, b"new");
    field(&mut next, b"c");
    field(&mut next, b"other");
    assert_eq!(s.read(7), next);
}
