namespace AMCStream {
using System;
using System.Collections.Concurrent;
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
    internal static SafeFileHandle OwnWorker() {
        var job=CreateJobObject(IntPtr.Zero,null);if(job.IsInvalid){job.Dispose();throw new IOException("worker_job_unavailable");}
        var limits=new ExtendedLimits{Basic=new Limits{Flags=0x2008,ActiveProcesses=32}}; // KILL_ON_JOB_CLOSE, bounded own descendants
        if(!SetInformationJobObject(job,9,ref limits,(uint)Marshal.SizeOf(typeof(ExtendedLimits)))||!AssignProcessToJobObject(job,GetCurrentProcess())){job.Dispose();throw new IOException("worker_job_unavailable");}
        return job;
    }
    // A bounded pump detects SSH stdin EOF even during an executing pipeline.
    // Closing the unnamed worker job terminates only this worker and descendants.
    internal sealed class Input : Stream {
        readonly BlockingCollection<byte[]> blocks=new BlockingCollection<byte[]>(2);
        byte[] current;int position;
        internal Input(Stream source,SafeFileHandle job) {
            var reader=new Thread(delegate(){
                try {for(;;){byte[] buffer=new byte[65536];int n=source.Read(buffer,0,buffer.Length);if(n==0)break;if(n!=buffer.Length)Array.Resize(ref buffer,n);blocks.Add(buffer);}}
                finally {blocks.CompleteAdding();job.Dispose();}
            });reader.IsBackground=true;reader.Start();
        }
        public override int Read(byte[] buffer,int offset,int count){
            if(count==0)return 0;
            if(current==null||position==current.Length){try{current=blocks.Take();position=0;}catch(InvalidOperationException){return 0;}}
            int n=Math.Min(count,current.Length-position);Buffer.BlockCopy(current,position,buffer,offset,n);position+=n;return n;
        }
        public override bool CanRead{get{return true;}}public override bool CanSeek{get{return false;}}public override bool CanWrite{get{return false;}}
        public override long Length{get{throw new NotSupportedException();}}public override long Position{get{throw new NotSupportedException();}set{throw new NotSupportedException();}}
        public override void Flush(){}public override long Seek(long offset,SeekOrigin origin){throw new NotSupportedException();}
        public override void SetLength(long size){throw new NotSupportedException();}public override void Write(byte[] buffer,int offset,int count){throw new NotSupportedException();}
    }
}
}
