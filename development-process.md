# Development Process

## Milestone 2
Implementing APIs of the various providers, Check if public api is available for use. if not available impossible to use as source.

Surf Has Public API will be included in the integration.
Levvy Doesn't seem to have a public API, possible maybe through onchain. 
Liqwid Has Public API will be included in the integration


Implement api endpoints to check availability of pools across sources.
All the Leverage Trading implementations up until Milestone 5 will be ADA-SNEK only.

Create Simple frontend (not final frontend, just a showcase of function and test environment) to inspect pools/ Search by pair, and analyze own wallet. Place Simple Borrows and Lends. 


Manual TESTING
LEND ADA						SURF	OK	LIQWID OK
LEND SNEK						SURF	OK	LIQWID OK
WITHDRAW ADA					SURF	OK	LIQWID OK
WITHDRAW SNEK 				    SURF	OK	LIQWID OK
BORROW ADA AGAINST SNEK		    SURF	OK	LIQWID OK
BORROW SNEK AGAINST ADA		    SURF	OK	LIQWID OK

To Test:
```
    go run main.go api
```
    
Open Chrome and go to http://localhost:8080
There the simple integrations of lends and borrows can be tested. This is not the final UI, it is merely a way to test the fucntions and api calls.
    

## Milestone 3
Implement leverage Engine that implements the flow as displayed in the diagram
Add custom wallet creation in api
Create Long and shorts On SNEK and NIGHT.
Create single Frontend to test and check data.
Test EtoE
Add tests


## Milestone 4
Create Proper Frontend


## Milestone 5
Fine Tuning and testing EtoE Full app.
