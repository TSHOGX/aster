import {expect,it,vi} from "vitest";
import {contentDigest,PreviewRepresentation} from "./preview-representation";
it("hashes exact bytes and retains URLs through asynchronous renderer teardown",async()=>{
 const one=new Blob(["same"]),two=new Blob(["size"]);
 expect(await contentDigest(one)).not.toBe(await contentDigest(two));
 expect(await contentDigest(one)).toBe(await contentDigest(new Blob(["same"])));
 const revoke=vi.spyOn(URL,"revokeObjectURL");
 const rep=new PreviewRepresentation(one,await contentDigest(one),"same","a.txt","text/plain");
 const renderer=rep.retain();
 rep.release();expect(revoke).not.toHaveBeenCalled();
 renderer();renderer();expect(revoke).toHaveBeenCalledTimes(1);
 expect(()=>rep.retain()).toThrow("released");
 revoke.mockRestore();
});
