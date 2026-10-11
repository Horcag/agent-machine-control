using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Text;
using System.Security.Cryptography;
using System.Security.Principal;
using System.Web.Script.Serialization;
using System.Management.Automation;
using System.Management.Automation.Runspaces;

namespace AMCStream {
public static class Worker {
    static readonly JavaScriptSerializer JSON = new JavaScriptSerializer { MaxJsonLength = 262144 };
    internal const string EmptyDigest="e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855";
    static Stream input, output;
    static Dictionary<string,object> identity;
    static Runspace runspace;
    static bool responseStarted;
    static byte[] Exact(int count) {return Exact(input,count);}
    static byte[] Exact(Stream from,int count) {
        byte[] data = new byte[count]; int position = 0;
        while (position < count) { int n = from.Read(data, position, count-position); if (n == 0) throw new EndOfStreamException(); position += n; }
        return data;
    }
    internal sealed class ControlFailure : IOException {
        internal readonly string Stage;
        internal ControlFailure(string stage){Stage=stage;}
    }
    static byte[] ControlBytes(Stream from,int count) {
        try{return Exact(from,count);}
        catch(EndOfStreamException){throw new ControlFailure("control_eof");}
        catch{throw new ControlFailure("control_read");}
    }
    internal static Dictionary<string,object> ReadControl(Stream from,Func<string,Dictionary<string,object>> decode) {
        byte[] h=ControlBytes(from,4);Array.Reverse(h);uint n=BitConverter.ToUInt32(h,0);
        if(n==0||n>262144)throw new ControlFailure("control_header");
        byte[] bytes=ControlBytes(from,(int)n);
        try{var value=decode(new UTF8Encoding(false,true).GetString(bytes));if(value==null)throw new InvalidDataException();return value;}
        catch{throw new ControlFailure("control_decode");}
    }
    static void Control(object value) {
        byte[] body = Encoding.UTF8.GetBytes(JSON.Serialize(value)); if (body.Length > 262144) throw new InvalidDataException();
        responseStarted=true;
        byte[] h = BitConverter.GetBytes((uint)body.Length); Array.Reverse(h); output.Write(h,0,4); output.Write(body,0,body.Length); output.Flush();
    }
    static string Text(Dictionary<string,object> value,string key) { object v; return value.TryGetValue(key,out v) ? Convert.ToString(v,System.Globalization.CultureInfo.InvariantCulture) : ""; }
    static long Number(Dictionary<string,object> value,string key) { object v; return value.TryGetValue(key,out v) ? Convert.ToInt64(v,System.Globalization.CultureInfo.InvariantCulture) : 0; }
    static DateTime Deadline(Dictionary<string,object> request) { return DateTime.Parse(Text(request,"deadlineUtc"),null,System.Globalization.DateTimeStyles.RoundtripKind).ToUniversalTime(); }
    static void Fresh(DateTime deadline) { if (DateTime.UtcNow >= deadline) throw new TimeoutException(); }
    static string Digest(byte[] bytes) { using(var hash=SHA256.Create()) return Hex(hash.ComputeHash(bytes)); }
    internal static string Hex(byte[] bytes) { return BitConverter.ToString(bytes).Replace("-","").ToLowerInvariant(); }
    static string Transfer(Stream from,Stream to,long size,DateTime deadline) {
        byte[] buffer = new byte[65536]; using(var hash=SHA256.Create()) {
            while(size>0) { Fresh(deadline); int n=from.Read(buffer,0,(int)Math.Min(buffer.Length,size)); if(n==0) throw new EndOfStreamException(); to.Write(buffer,0,n); hash.TransformBlock(buffer,0,n,null,0);size-=n; }
            hash.TransformFinalBlock(new byte[0],0,0);return Hex(hash.Hash);
        }
    }
    static Dictionary<string,object> Result(string id,string status,long size,string hash,int exit) {
        return new Dictionary<string,object>{{"id",id},{"status",status},{"size",size},{"sha256",hash},{"exitCode",exit},{"worker",identity}};
    }
    static void Empty(Dictionary<string,object> result) {
        result["size"]=0L;result["sha256"]=EmptyDigest;Control(result);Control(result);
    }
    public static void Run(Stream stdin,Stream stdout) {
        using(var lifetime=new Lifetime.Guard())Lifetime.RunWorker(delegate{RunOwned(stdin,stdout,lifetime);},lifetime.Termination);
    }
    static void RunOwned(Stream stdin,Stream stdout,Lifetime.Guard lifetime) {
        input=stdin;output=stdout;var process=Process.GetCurrentProcess();
        identity=new Dictionary<string,object>{{"pid",process.Id},{"birthTicks",process.StartTime.ToUniversalTime().Ticks},{"image",process.MainModule.FileName},{"sid",WindowsIdentity.GetCurrent().User.Value}};
        Control(identity);
        try {
            // Open the binary streams after the original hello. The inherited
            // pipe's native mode determines whether overlapped I/O is required.
            using(var binaryInput=Lifetime.StandardStream(-10,FileAccess.Read))
            using(var binaryOutput=Lifetime.StandardStream(-11,FileAccess.Write)) {
            input=binaryInput;output=binaryOutput;
            for(;;) {
                Dictionary<string,object> request;
                try{request=ReadControl(input,delegate(string text){return JSON.Deserialize<Dictionary<string,object>>(text);});}
                catch(ControlFailure error){
                    lifetime.ArmFailure(DateTime.MaxValue,Failure.TimerExit(error,error.Stage));
                    var fatal=Result("","failed",0,EmptyDigest,1);
                    fatal["controlFailure"]=new Dictionary<string,object>{{"stage",error.Stage}};
                    Empty(fatal);lifetime.HoldTerminal();return;
                }
                // Malformed or interrupted requests terminate the channel, never replay.
                string id=Text(request,"id");if(id.Length==0||id.Length>128)throw new InvalidDataException();
                responseStarted=false;string stage="request";DateTime deadline=DateTime.MaxValue;
                try {
                    deadline=Deadline(request);Fresh(deadline);
                    string operation=Text(request,"operation");long size=Number(request,"size"),limit=Number(request,"maxOutput");
                    if(size<0||size>(1L<<40)||limit<0||limit>(1L<<40))throw new InvalidDataException();
                    if(operation!="observe"&&operation!="put"&&operation!="get"&&operation!="exec"&&operation!="remove-owned")throw new InvalidDataException();
                    stage="runspace_open";
                    if(runspace==null){runspace=RunspaceFactory.CreateRunspace();runspace.Open();}
                    stage=operation=="remove-owned"?"remove_owned":operation;
                    if(operation=="observe") Observe(request,id,deadline);
                    else if(operation=="put") Put(request,id,size,deadline);
                    else if(operation=="get") Get(request,id,limit,deadline);
                    else if(operation=="exec") Exec(request,id,size,limit,deadline);
                    else Remove(request,id,deadline);
                } catch(Exception error) {
                    // A partial response cannot safely become a new control frame.
                    if(responseStarted)throw;
                    // Closing our kill-on-close job immediately after Flush can
                    // race the SSH server's pipe drain. Bound the terminal wait
                    // before writing, including output backpressure and cleanup.
                    lifetime.ArmFailure(deadline,Failure.TimerExit(error,stage));
                    var result=Result(id,"failed",0,EmptyDigest,1);
                    result["failure"]=Failure.Metadata(error,stage);Empty(result);Lifetime.DrainTerminal(input);return;
                }
            }
            }
        } finally {if(runspace!=null)runspace.Dispose();}
    }
    static void Observe(Dictionary<string,object> request,string id,DateTime deadline) {
        string path=Native.FullPath(Text(request,"path"));
        using(var roots=Native.LockParents(Path.Combine(path,".amc-observe-placeholder"))) {
            Fresh(deadline);var root=roots.Identity();Fresh(deadline);
            var result=Result(id,"ok",0,EmptyDigest,0);result["root"]=root;Empty(result);
        }
    }
    static void Put(Dictionary<string,object> request,string id,long size,DateTime deadline) {
        string path=Native.FullPath(Text(request,"path"));
        using(var roots=Native.LockParents(path)) {
            Fresh(deadline);
            var root=roots.Identity();if(!Native.Same((Dictionary<string,object>)request["expectedRoot"],root))throw new InvalidDataException();
            // CREATE_NEW refuses replacement. Incomplete bytes remain exclusively task-owned
            // and are removed by their held handle on every unsuccessful commit.
            using(var file=Native.Create(path,(string)root["owner"])) {
                string hash=Transfer(input,file.Stream,size,deadline);
                if(input.ReadByte()!=1||hash!=Text(request,"sha256")) throw new InvalidDataException();
                Fresh(deadline);file.Stream.Flush(true);
                var receipt=file.Identity(path,roots,hash);Fresh(deadline);file.Commit();
                var result=Result(id,"ok",0,EmptyDigest,0);result["files"]=new object[]{receipt};Empty(result);
            }
        }
    }
    static void Get(Dictionary<string,object> request,string id,long limit,DateTime deadline) {
        string path=Native.FullPath(Text(request,"path"));
        var owned=(System.Collections.IList)request["owned"];
        if(owned.Count!=1)throw new InvalidDataException();
        var expected=(Dictionary<string,object>)owned[0];
        using(var roots=Native.LockParents(path)) using(var file=Native.Open(path)) {
            long size=file.Stream.Length;if(size>limit)throw new InvalidDataException();
            Fresh(deadline);
            // Verify the held original file, root, security and contents before
            // emitting any bytes. A substituted path must not disclose data.
            var original=file.Identity(path,roots);
            if(!Native.Same(expected,original))throw new InvalidDataException();
            Fresh(deadline);Control(Result(id,"payload",size,"",0));
            string hash=Transfer(file.Stream,output,size,deadline);Fresh(deadline);
            var result=Result(id,"ok",size,hash,0);result["files"]=new object[]{file.Identity(path,roots,hash)};Control(result);
        }
    }
    static void Exec(Dictionary<string,object> request,string id,long size,long limit,DateTime deadline) {
        if(size>16777216||limit==0||limit>16777216)throw new InvalidDataException();
        byte[] script=Exact((int)size);if(input.ReadByte()!=1||Digest(script)!=Text(request,"sha256"))throw new InvalidDataException();
        Fresh(deadline);
        using(var shell=PowerShell.Create()) {
            shell.Runspace=runspace;runspace.SessionStateProxy.SetVariable("LASTEXITCODE",null);shell.AddScript(new UTF8Encoding(false,true).GetString(script));
            // Streaming the pipeline bounds retained output and observes completion.
            var values=new PSDataCollection<PSObject>();var text=new StringBuilder();bool overflow=false,hadError=false;long retained=0;var outputLock=new object();
            Action<string> append=delegate(string line){lock(outputLock){long added=Encoding.UTF8.GetByteCount(line);if(retained+added>limit){if(!overflow){overflow=true;shell.BeginStop(null,null);}}else{text.Append(line);retained+=added;}}};
            values.DataAdded += delegate(object sender,DataAddedEventArgs args) {
                foreach(var value in values.ReadAll())append(Convert.ToString(value)+Environment.NewLine);
            };
            shell.Streams.Error.DataAdded += delegate(object sender,DataAddedEventArgs args) {
                hadError=true;foreach(var error in shell.Streams.Error.ReadAll())append(error.ToString()+Environment.NewLine);
            };
            shell.Streams.Warning.DataAdded += delegate(object sender,DataAddedEventArgs args) {shell.Streams.Warning.ReadAll();};
            shell.Streams.Verbose.DataAdded += delegate(object sender,DataAddedEventArgs args) {shell.Streams.Verbose.ReadAll();};
            shell.Streams.Debug.DataAdded += delegate(object sender,DataAddedEventArgs args) {shell.Streams.Debug.ReadAll();};
            shell.Streams.Progress.DataAdded += delegate(object sender,DataAddedEventArgs args) {shell.Streams.Progress.ReadAll();};
            shell.Streams.Information.DataAdded += delegate(object sender,DataAddedEventArgs args) {
                foreach(var message in shell.Streams.Information.ReadAll())append(message.ToString()+Environment.NewLine);
            };
            IAsyncResult pending=shell.BeginInvoke<PSObject,PSObject>(null,values);
            int wait=(int)Math.Min(Int32.MaxValue,Math.Max(1,(deadline-DateTime.UtcNow).TotalMilliseconds));
            if(!pending.AsyncWaitHandle.WaitOne(wait)) {shell.Stop();throw new TimeoutException();}
            try {shell.EndInvoke(pending);} catch(PipelineStoppedException){if(!overflow)throw;}
            byte[] payload=Encoding.UTF8.GetBytes(text.ToString());Fresh(deadline);
            object nativeExit=runspace.SessionStateProxy.GetVariable("LASTEXITCODE");int exit=hadError||shell.HadErrors?1:(nativeExit==null?0:Convert.ToInt32(nativeExit));var result=Result(id,overflow?"output_limit":"ok",payload.Length,Digest(payload),exit);
            Control(result);output.Write(payload,0,payload.Length);Control(result);
        }
    }
    static void Remove(Dictionary<string,object> request,string id,DateTime deadline) {
        var entries=(System.Collections.IList)request["owned"];if(entries.Count==0||entries.Count>128)throw new InvalidDataException();
        var removed=new List<object>();
        foreach(Dictionary<string,object> expected in entries) {
            string path=Text(expected,"path");string status="preserved";bool absent=false;
            try {
                path=Native.FullPath(path);
                using(var roots=Native.LockParents(path)) using(var file=Native.Open(path)) {
                    Fresh(deadline);var actual=file.Identity(path,roots);
                    if(Native.Same(expected,actual)) {Fresh(deadline);file.Delete();status="deleted";}
                }
                absent=Native.Absent(path);if(status=="deleted"&&!absent)status="absence_unconfirmed";
            } catch(TimeoutException){status="expired";} catch {status="preserved";}
            removed.Add(new Dictionary<string,object>{{"path",path},{"status",status},{"absent",absent}});
        }
        var result=Result(id,"ok",0,EmptyDigest,0);result["removed"]=removed;Empty(result);
    }
}
}
