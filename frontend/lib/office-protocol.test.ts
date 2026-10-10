import { expect, it } from 'vitest';
import { officeKind, validPosition } from '../office/protocol';
it('classifies supported OOXML independently of size and rejects legacy formats', () => {
  expect(['report.DOCX','book.xlsx','deck.pptx','old.xls','macro.xlsm'].map(officeKind)).toEqual(['document','spreadsheet','presentation',null,null]);
});
it('rejects untrusted position messages before restoring browser state', () => {
  expect(validPosition({top:0,left:100,slide:199,focus:'next'})).toBe(true);
  for (const value of [null,{}, {top:NaN,left:0,slide:1},{top:0,left:0,slide:0.5},{top:0,left:0,slide:10000},{top:0,left:0,slide:1,focus:'x'.repeat(128)}]) expect(validPosition(value)).toBe(false);
});
