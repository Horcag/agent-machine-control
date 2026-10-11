namespace AMCStream {
using System;
using System.IO;
using System.Runtime.InteropServices;
using System.Threading;
using Microsoft.Win32.SafeHandles;
internal static class Lifetime {
    [StructLayout(LayoutKind.Sequential)] struct Limits {
        public long ProcessTime,JobTime;public uint Flags;public UIntPtr Minimum,Maximum;
        public uint ActiveProcesses;public UIntPtr Affinity;public uint Priority,Scheduling;
    }
    [StructLayout(LayoutKind.Sequential)] struct Counters {public ulong ReadOperations,WriteOperations,OtherOperations,ReadBytes,WriteBytes,OtherBytes;}
    [StructLayout(LayoutKind.Sequential)] struct ExtendedLimits {public Limits Basic;public Counters IO;public UIntPtr ProcessMemory,JobMemory,PeakProcessMemory,PeakJobMemory;}
    [DllImport("kernel32.dll",CharSet=CharSet.Unicode,SetLastError=true)] static extern SafeFileHandle CreateJobObject(IntPtr security,string name);
    [DllImport("kernel32.dll",SetLastError=true)] static extern bool SetInformationJobObject(SafeFileHandle job,int type,ref ExtendedLimits limits,uint length);
    [DllImport("kernel32.dll",SetLastError=true)] static extern bool AssignProcessToJobObject(SafeFileHandle job,IntPtr process);
    [DllImport("kernel32.dll")] static extern IntPtr GetCurrentProcess();
    [DllImport("kernel32.dll",SetLastError=true)] static extern bool TerminateJobObject(SafeFileHandle job,uint exitCode);
    internal static SafeFileHandle OwnWorker() {
        var job=CreateJobObject(IntPtr.Zero,null);if(job.IsInvalid){job.Dispose();throw new IOException("worker_job_unavailable");}
        var limits=new ExtendedLimits{Basic=new Limits{Flags=0x2008,ActiveProcesses=32}}; // KILL_ON_JOB_CLOSE, bounded own descendants
        if(!SetInformationJobObject(job,9,ref limits,(uint)Marshal.SizeOf(typeof(ExtendedLimits)))||!AssignProcessToJobObject(job,GetCurrentProcess())){job.Dispose();throw new IOException("worker_job_unavailable");}
        return job;
    }
    [StructLayout(LayoutKind.Sequential)] struct PipeLocal {
        public uint Type,Configuration,MaximumInstances,CurrentInstances,InboundQuota,ReadAvailable,OutboundQuota,WriteAvailable,State,End;
    }
    [StructLayout(LayoutKind.Sequential)] struct IOStatus {public IntPtr Status;public UIntPtr Information;}
    [DllImport("kernel32.dll",SetLastError=true)] static extern IntPtr GetStdHandle(int id);
    [DllImport("kernel32.dll",SetLastError=true)] static extern uint GetFileType(IntPtr handle);
    [DllImport("ntdll.dll")] static extern int NtQueryInformationFile(IntPtr handle,out IOStatus status,out PipeLocal pipe,uint length,int type);
    internal static uint PipeState(IntPtr handle) {
        IOStatus status;PipeLocal pipe;
        int result=NtQueryInformationFile(handle,out status,out pipe,40,24); // FilePipeLocalInformation
        return CheckedPipeState(result,status.Information.ToUInt64(),pipe.State);
    }
    internal const uint QueryFailureExit=211,ShortInformationExit=212,DisconnectedExit=213,PendingExit=214;
    internal sealed class PipeFailure : IOException {
        internal readonly uint ExitCode;
        internal PipeFailure(uint exitCode){ExitCode=exitCode;}
    }
    internal static uint CheckedPipeState(int status,ulong information,uint state) {
        if(status<0)throw new PipeFailure(QueryFailureExit);
        if(status==0x103)throw new PipeFailure(PendingExit);
        if(information<40)throw new PipeFailure(ShortInformationExit);
        return state;
    }
    internal sealed class OwnTermination {
        readonly Func<uint,bool> terminate;readonly Action close;int stopped;
        internal OwnTermination(Func<uint,bool> terminate,Action close){this.terminate=terminate;this.close=close;}
        internal void Stop(uint code){
            if(Interlocked.Exchange(ref stopped,1)!=0)return;
            try{terminate(code);}catch{}finally{close();}
        }
    }
    internal static void Observe(WaitHandle stopped,Func<uint> query,Action disconnect) {
        while(!stopped.WaitOne(50)){if(query()!=3){disconnect();return;}}
    }
    internal static TimeSpan FailureBudget(DateTime deadline,DateTime now) {
        return TimeSpan.FromTicks(Math.Min(TimeSpan.TicksPerSecond,Math.Max(0,deadline.Ticks-now.Ticks)));
    }
    internal static Timer ArmFailureBound(Action close,DateTime deadline) {
        return new Timer(delegate{close();},null,FailureBudget(deadline,DateTime.UtcNow),Timeout.InfiniteTimeSpan);
    }
    // After a failed response the worker is terminal. Keep the pipe open long
    // enough for the peer to consume the pair; no further request is executed.
    internal static void DrainTerminal(Stream input) {while(input.ReadByte()>=0){}}
    // Observe the pipe's native connection state without consuming bytes. A full
    // input backlog cannot conceal EOF behind a bounded producer queue.
    internal sealed class Guard : IDisposable {
        readonly SafeFileHandle job;readonly ManualResetEvent stopped=new ManualResetEvent(false);readonly Thread observer;Timer terminal;
        internal Guard() {
            IntPtr stdin=GetStdHandle(-10);
            if(stdin==IntPtr.Zero||stdin==new IntPtr(-1)||GetFileType(stdin)!=3||PipeState(stdin)!=3)throw new IOException("stdin_lifecycle_unavailable");
            job=OwnWorker();
            var termination=new OwnTermination(delegate(uint code){return TerminateJobObject(job,code);},delegate{job.Dispose();});
            observer=new Thread(delegate(){
                try{Observe(stopped,delegate(){return PipeState(stdin);},delegate(){termination.Stop(DisconnectedExit);});}
                catch(PipeFailure error){termination.Stop(error.ExitCode);}
                catch{job.Dispose();}
            });observer.IsBackground=true;
            try{observer.Start();}catch{job.Dispose();stopped.Dispose();throw;}
        }
        internal void ArmFailure(DateTime deadline){terminal=ArmFailureBound(delegate{job.Dispose();},deadline);}
        public void Dispose(){
            stopped.Set();
            // Closing this exact unnamed job is the fail-closed bound if a native
            // query fails to return; no observer or descendant survives the worker.
            if(!observer.Join(2000)){job.Dispose();if(terminal!=null)terminal.Dispose();return;}
            stopped.Dispose();job.Dispose();if(terminal!=null)terminal.Dispose();
        }
    }
}
}
