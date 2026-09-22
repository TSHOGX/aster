/** Immutable bytes own their URL until the last renderer releases its lease. */
export class PreviewRepresentation {
  readonly url: string;
  private refs=1;
  constructor(readonly blob:Blob,readonly digest:string,readonly text:string,readonly path:string,mime:string) {
    this.url=URL.createObjectURL(new Blob([blob],{type:mime}));
  }
  retain() {
    if(!this.refs) throw new Error("Preview already released");
    this.refs++;
    let released=false;
    return ()=>{if(!released){released=true;this.release();}};
  }
  release() { if(this.refs>0 && --this.refs===0) URL.revokeObjectURL(this.url); }
}
export async function contentDigest(blob:Blob) {
  const digest=await crypto.subtle.digest("SHA-256",await blob.arrayBuffer());
  return Array.from(new Uint8Array(digest),b=>b.toString(16).padStart(2,"0")).join("");
}
