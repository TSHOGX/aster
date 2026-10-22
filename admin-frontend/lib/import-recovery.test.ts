import {expect,it} from "vitest";
import {readImportCommand,importEndpoint,type ImportCommand} from "./import-recovery";
it("scopes exact import commands and never recovers an arbitrary route",()=>{
 const body={request_id:"original",source_id:"source",version_id:"release",expected_manifest_digest:`sha256:${"a".repeat(64)}`,consumer_profile:"authored-v1"};
 const command:ImportCommand={account:"admin",action:"admit",imported:"",body:JSON.stringify(body,null,2)};
 expect(readImportCommand(JSON.stringify(command),"admin")).toEqual(command);
 expect(importEndpoint(command)).toBe("/api/admin/volume-imports");
 for(const changes of [{account:"other"},{action:"/api/admin/users"},{imported:"foreign"},{body:"null"},{body:JSON.stringify({...body,expected_manifest_digest:"invalid"})}])expect(()=>readImportCommand(JSON.stringify({...command,...changes}),"admin")).toThrow();
 for(const action of ["retry","cancel","retire"] as const){const lifecycle={...command,action,imported:"one",body:'{ "request_id": "same" }'};expect(readImportCommand(JSON.stringify(lifecycle),"admin")).toEqual(lifecycle);expect(importEndpoint(lifecycle)).toBe(`/api/admin/volume-imports/one/${action}`);expect(()=>readImportCommand(JSON.stringify({...lifecycle,imported:"../users"}),"admin")).toThrow();}
 expect(()=>readImportCommand(" ".repeat(8193),"admin")).toThrow();
});
