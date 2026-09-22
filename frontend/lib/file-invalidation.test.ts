import {expect,it} from "vitest";
import {fileImpact} from "./file-invalidation";
it("keeps unrelated writes away from the selected body and routes rename descendants",()=>{
 for(let n=0;n<100;n++) expect(fileImpact({path:`other/${n}`,operation:"write"},"reports/a.md",["","reports","other"]).selected).toBe(false);
 expect(fileImpact({path:"archive",old_path:"reports",operation:"rename",entry_type:"directory"},"reports/a.md",["","reports","reports/sub","other"])).toEqual({gap:false,selected:true,directories:["","reports","reports/sub"]});
 expect(fileImpact({reset:true},"reports/a.md",Array.from({length:100},(_,n)=>`${n}`)).directories).toHaveLength(16);
});
