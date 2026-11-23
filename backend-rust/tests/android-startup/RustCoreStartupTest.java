package com.smartvpn.client;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.atomic.AtomicReference;

public final class RustCoreStartupTest {
 static void check(boolean v){if(!v)throw new AssertionError();}
 static class Fake implements RustCoreStartup.NativeCalls {
  int configurations,starts,result; boolean legacy; String path; List<String> order=new ArrayList<String>();
  public int configure(byte[] bytes,boolean enabled){check(enabled);configurations++;order.add("configure");path=new String(bytes,StandardCharsets.UTF_8);if(legacy)throw new UnsatisfiedLinkError("old");return result;}
  public String start(String dir,String name){check(dir.equals("synthetic-data")&&name.equals("bridge"));starts++;order.add("start");return "bootstrap";}
 }
 static String run(RustCoreStartup s,boolean enabled,String dir,Fake f){return s.start(enabled,dir,"synthetic-data","bridge",f);}
 public static void main(String[] args)throws Exception {
  String bundle=new java.io.File("synthetic-bundle").getAbsolutePath();
  RustCoreStartup s=new RustCoreStartup();Fake f=new Fake();check(run(s,false,bundle,f).equals("bootstrap"));check(f.configurations==0&&f.starts==1&&s.status().equals("go_default"));
  for(int code:new int[]{0,-1,-2,-3,-4}){
   s=new RustCoreStartup();f=new Fake();f.result=code;run(s,true,bundle,f);run(s,true,bundle,f);
   check(f.configurations==1&&f.starts==2&&f.order.get(0).equals("configure")&&f.order.get(1).equals("start"));
   check(f.path.equals(new java.io.File(bundle,"libsmartvpn_core.so").getPath()));
   check(s.status().equals(code==0?"rust_requested":code==-2?"configuration_frozen":"go_fallback_configure_"+code));
  }
  s=new RustCoreStartup();f=new Fake();f.legacy=true;run(s,true,bundle,f);check(f.starts==1&&s.status().equals("go_fallback_legacy_library"));
  for(String invalid:new String[]{null,"relative","bad\0path"}){s=new RustCoreStartup();f=new Fake();run(s,true,invalid,f);check(f.configurations==0&&f.starts==1&&s.status().equals("go_fallback_invalid_bundle_directory"));}
  final RustCoreStartup shared=new RustCoreStartup(); final Fake fake=new Fake();final CountDownLatch ready=new CountDownLatch(1);final AtomicReference<Throwable> failure=new AtomicReference<Throwable>();List<Thread> threads=new ArrayList<Thread>();
  for(int i=0;i<32;i++){Thread t=new Thread(()->{try{ready.await();run(shared,true,bundle,fake);}catch(Throwable e){failure.set(e);}});threads.add(t);t.start();}
  ready.countDown();for(Thread t:threads)t.join();if(failure.get()!=null)throw new AssertionError(failure.get());check(fake.configurations==1&&fake.starts==32&&fake.order.get(0).equals("configure"));
  // Every request retains original idempotent start, including stop/restart use;
  // configuration failure never causes a duplicate start or automatic stop.
  for (final boolean fatal : new boolean[]{false,true}) {
   final RustCoreStartup guarded = new RustCoreStartup();
   Fake throwing = new Fake() {
    public int configure(byte[] bytes, boolean enabled) {
     super.configure(bytes, enabled);
     if(fatal) throw new AssertionError("after side effect");
     throw new IllegalStateException("after side effect");
    }
   };
   try { run(guarded,true,bundle,throwing); throw new AssertionError("expected failure"); }
   catch (RuntimeException | Error expected) { check(expected.getMessage().equals("after side effect")); }
   check(throwing.configurations==1 && throwing.starts==0 && guarded.status().equals("configuration_uncertain"));
   try { run(guarded,true,bundle,throwing); throw new AssertionError("expected fail closed"); }
   catch (IllegalStateException expected) { check(expected.getMessage().contains("uncertain")); }
   check(throwing.configurations==1 && throwing.starts==0);
  }
  System.out.println("RustCoreStartup: default, 5 statuses, legacy, 3 invalid paths, ordering, repeated calls, 32 concurrent requests, 2 side-effect-then-throw fail-closed scenarios PASS");
 }
}
